// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const MarketplaceFilename = "ocr-marketplace.json"

// DisplaySource removes URL credentials and query values from diagnostics and
// listings. Local filesystem paths and catalog origins keep their spelling.
func DisplaySource(source string) string {
	u, err := url.Parse(source)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return source
	}
	return displayURL(u)
}

func displayURL(u *url.URL) string {
	u.User = nil
	u.RawQuery = ""
	u.ForceQuery = false
	u.Fragment = ""
	u.RawFragment = ""
	return u.String()
}

type downloadError struct {
	message string
	cause   error
}

type httpStatusError struct {
	status int
	source string
}

func (e httpStatusError) Error() string {
	return fmt.Sprintf("download %s: HTTP %d", DisplaySource(e.source), e.status)
}

func (e downloadError) Error() string { return e.message }
func (e downloadError) Unwrap() error { return e.cause }

func redactDownloadError(err error, source string) error {
	message := strings.ReplaceAll(err.Error(), source, DisplaySource(source))
	for cause := err; cause != nil; cause = errors.Unwrap(cause) {
		if requestError, ok := cause.(*url.Error); ok {
			// net/http may report the relative Location value for a redirect,
			// rather than an absolute URL. It still carries a URL query.
			if parsed, parseErr := url.Parse(requestError.URL); parseErr == nil {
				message = strings.ReplaceAll(message, requestError.URL, displayURL(parsed))
			}
		}
	}
	return downloadError{message: message, cause: err}
}

// CatalogEntry is a cached catalog record. Its source is resolved against the
// catalog location before it is saved, and Market names its registry entry.
type CatalogEntry struct {
	Market      string `json:"market,omitempty"`
	ID          string `json:"id"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description,omitempty"`
	Source      string `json:"source"`
	SHA256      string `json:"sha256,omitempty"`
}

// Market is the saved catalog snapshot; searching it never makes a request.
type Market struct {
	Name          string         `json:"name"`
	Source        string         `json:"source"`
	SchemaVersion int            `json:"schema_version"`
	Plugins       []CatalogEntry `json:"plugins"`
	RefreshedAt   time.Time      `json:"refreshed_at"`
}

type catalog struct {
	SchemaVersion int             `json:"schema_version"`
	Name          string          `json:"name"`
	Plugins       []catalogPlugin `json:"plugins"`
}

// Separate wire type keeps cached metadata out of the published catalog schema.
type catalogPlugin struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description,omitempty"`
	Source      string `json:"source"`
	SHA256      string `json:"sha256,omitempty"`
}

func canonicalSource(source, defaultFilename string) (string, bool, error) {
	if strings.HasPrefix(strings.ToLower(source), "https:") {
		if err := validateHTTPS(source); err != nil {
			return "", false, err
		}
		return source, true, nil
	}
	// Do not interpret another URL scheme as a local filename. Windows drive
	// letters are still accepted by filepath.IsAbs on Windows.
	if strings.Contains(source, "://") || strings.HasPrefix(strings.ToLower(source), "http:") {
		return "", false, fmt.Errorf("remote sources must use HTTPS")
	}
	if strings.TrimSpace(source) == "" {
		return "", false, fmt.Errorf("source is required")
	}
	absolute, err := filepath.Abs(source)
	if err != nil {
		return "", false, err
	}
	if defaultFilename != "" {
		info, err := os.Stat(absolute)
		if err != nil {
			return "", false, err
		}
		if info.IsDir() {
			absolute = filepath.Join(absolute, defaultFilename)
		}
	}
	return absolute, false, nil
}

func validateHTTPS(source string) error {
	u, err := url.Parse(source)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return fmt.Errorf("remote source must be an HTTPS URL without credentials or fragment")
	}
	return nil
}

func (s *Store) download(ctx context.Context, source string, limit int64) ([]byte, string, error) {
	if err := validateHTTPS(source); err != nil {
		return nil, "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return nil, "", err
	}
	client := *s.client
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("too many redirects")
		}
		return validateHTTPS(req.URL.String())
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, "", fmt.Errorf("download %s: %w", DisplaySource(source), redactDownloadError(err, source))
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, "", httpStatusError{status: response.StatusCode, source: source}
	}
	if response.ContentLength > limit {
		return nil, "", fmt.Errorf("download exceeds %d byte limit", limit)
	}
	data, err := readBounded(response.Body, limit)
	if err != nil {
		return nil, "", fmt.Errorf("download %s: %w", DisplaySource(source), redactDownloadError(err, source))
	}
	return data, response.Request.URL.String(), nil
}

func (s *Store) loadMarket(ctx context.Context, name, source string) (Market, error) {
	result := Market{Name: name}
	if !validID(name) {
		return result, fmt.Errorf("invalid marketplace name %q", name)
	}
	resolved, remote, err := canonicalSource(source, MarketplaceFilename)
	if err != nil {
		return result, err
	}
	var data []byte
	catalogSource := resolved
	if remote {
		data, catalogSource, err = s.download(ctx, resolved, maxCatalogBytes)
	} else {
		data, err = readFileBounded(resolved, maxCatalogBytes)
	}
	if err != nil {
		return result, err
	}
	result, err = parseMarket(name, catalogSource, data, remote)
	// Refresh the subscribed URL so redirects can track new releases.
	result.Source = resolved
	return result, err
}

func parseMarket(name, resolved string, data []byte, remote bool) (Market, error) {
	result := Market{Name: name}
	var err error
	var published catalog
	if err := decodeStrict(data, &published); err != nil {
		return result, fmt.Errorf("parse marketplace: %w", err)
	}
	if published.SchemaVersion != 1 || strings.TrimSpace(published.Name) == "" {
		return result, fmt.Errorf("marketplace requires schema_version 1 and a name")
	}
	result.Source, result.SchemaVersion = resolved, 1
	result.RefreshedAt = time.Now().UTC()
	seen := make(map[string]bool)
	for _, item := range published.Plugins {
		if !validID(item.ID) || strings.TrimSpace(item.Name) == "" || strings.TrimSpace(item.Version) == "" || strings.TrimSpace(item.Source) == "" {
			return result, fmt.Errorf("invalid marketplace entry %q: id, name, version and source are required", item.ID)
		}
		if seen[item.ID] {
			return result, fmt.Errorf("duplicate marketplace plugin %q", item.ID)
		}
		seen[item.ID] = true
		if item.SHA256 != "" {
			if _, err := parseDigest(item.SHA256); err != nil {
				return result, fmt.Errorf("plugin %q: %w", item.ID, err)
			}
		}
		if remote {
			base, _ := url.Parse(resolved)
			entryURL, err := url.Parse(item.Source)
			if err != nil {
				return result, fmt.Errorf("plugin %q: invalid source URL", item.ID)
			}
			item.Source = base.ResolveReference(entryURL).String()
		}
		if remote || strings.HasPrefix(strings.ToLower(item.Source), "https:") {
			if err := validateHTTPS(item.Source); err != nil {
				return result, fmt.Errorf("plugin %q: %w", item.ID, err)
			}
			if item.SHA256 == "" {
				return result, fmt.Errorf("plugin %q: remote archive source requires sha256", item.ID)
			}
		} else {
			if strings.Contains(item.Source, "://") {
				return result, fmt.Errorf("plugin %q: remote sources must use HTTPS", item.ID)
			}
			if !filepath.IsAbs(item.Source) {
				item.Source = filepath.Join(filepath.Dir(resolved), item.Source)
			}
			item.Source, _, err = canonicalSource(item.Source, "")
			if err != nil {
				return result, err
			}
		}
		result.Plugins = append(result.Plugins, CatalogEntry{Market: name, ID: item.ID, Name: item.Name, Version: item.Version, Description: item.Description, Source: item.Source, SHA256: strings.ToLower(item.SHA256)})
	}
	return result, nil
}

func (s *Store) AddMarket(ctx context.Context, name, source string) (Market, error) {
	unlock, err := s.lock(ctx)
	if err != nil {
		return Market{}, err
	}
	defer unlock()
	value, err := s.readState()
	if err != nil {
		return Market{}, err
	}
	if _, exists := value.Markets[name]; exists {
		return Market{}, fmt.Errorf("marketplace %q already exists; use refresh", name)
	}
	market, err := s.loadMarket(ctx, name, source)
	if err != nil {
		return Market{}, err
	}
	value.Markets[name] = market
	return market, s.writeState(value)
}

func (s *Store) ListMarkets() ([]Market, error) {
	value, err := s.readState()
	if err != nil {
		return nil, err
	}
	items := make([]Market, 0, len(value.Markets))
	for _, market := range value.Markets {
		items = append(items, market)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return items, nil
}

func (s *Store) RefreshMarket(ctx context.Context, name string) (Market, error) {
	unlock, err := s.lock(ctx)
	if err != nil {
		return Market{}, err
	}
	defer unlock()
	value, err := s.readState()
	if err != nil {
		return Market{}, err
	}
	old, ok := value.Markets[name]
	if !ok {
		return Market{}, fmt.Errorf("marketplace %q does not exist", name)
	}
	market, err := s.loadMarket(ctx, name, old.Source)
	if err != nil {
		return Market{}, err
	}
	value.Markets[name] = market
	return market, s.writeState(value)
}

func (s *Store) RemoveMarket(name string) error {
	unlock, err := s.lock(context.Background())
	if err != nil {
		return err
	}
	defer unlock()
	value, err := s.readState()
	if err != nil {
		return err
	}
	if _, ok := value.Markets[name]; !ok {
		return fmt.Errorf("marketplace %q does not exist", name)
	}
	delete(value.Markets, name)
	return s.writeState(value)
}

func (s *Store) Search(query string) ([]CatalogEntry, error) {
	markets, err := s.ListMarkets()
	if err != nil {
		return nil, err
	}
	query = strings.ToLower(query)
	result := make([]CatalogEntry, 0)
	for _, market := range markets {
		for _, item := range market.Plugins {
			if strings.Contains(strings.ToLower(item.ID+" "+item.Name+" "+item.Description), query) {
				result = append(result, item)
			}
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].ID != result[j].ID {
			return result[i].ID < result[j].ID
		}
		return result[i].Market < result[j].Market
	})
	return result, nil
}

func resolveInstallSource(source string, markets map[string]Market) (resolved, origin string, expected CatalogEntry, err error) {
	// A saved catalog origin stays a catalog origin even if a local directory
	// later acquires the same name. Use ./name@market for a local directory.
	if id, name, ok := strings.Cut(source, "@"); ok && validID(id) && validID(name) {
		market, exists := markets[name]
		if !exists {
			return "", "", expected, fmt.Errorf("marketplace %q does not exist", name)
		}
		for _, item := range market.Plugins {
			if item.ID == id {
				return item.Source, source, item, nil
			}
		}
		return "", "", expected, fmt.Errorf("plugin %q is absent from marketplace %q", id, name)
	}
	resolved, _, err = canonicalSource(source, "")
	return resolved, resolved, expected, err
}

func parseDigest(value string) ([]byte, error) {
	digest, err := hex.DecodeString(value)
	if err != nil || len(digest) != sha256.Size {
		return nil, fmt.Errorf("sha256 must contain exactly 64 hexadecimal characters")
	}
	return digest, nil
}

func (s *Store) prepare(ctx context.Context, source, digest, stage string) error {
	_, err := s.preparePackage(ctx, source, digest, stage)
	return err
}

func (s *Store) preparePackage(ctx context.Context, source, digest, stage string) (string, error) {
	if strings.HasPrefix(strings.ToLower(source), "https:") {
		data, _, err := s.download(ctx, source, maxArchiveBytes)
		if err != nil {
			return "", err
		}
		if digest != "" {
			if err := verifyDigest(data, digest); err != nil {
				return "", err
			}
		}
		return archiveDigest(data), extractArchive(ctx, data, stage)
	}
	info, err := os.Lstat(source)
	if err != nil {
		return "", fmt.Errorf("read plugin source: %w", err)
	}
	if info.IsDir() {
		if digest != "" {
			return "", fmt.Errorf("sha256 applies to archives, not directory sources")
		}
		absoluteStore, err := filepath.Abs(s.root)
		if err != nil {
			return "", err
		}
		rel, err := filepath.Rel(source, absoluteStore)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return "", fmt.Errorf("plugin source directory cannot contain the plugin store")
		}
		absoluteStage, err := filepath.Abs(stage)
		if err != nil {
			return "", err
		}
		rel, err = filepath.Rel(source, absoluteStage)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return "", fmt.Errorf("plugin source directory cannot contain the staging directory")
		}
		root, err := openPackageRoot(source)
		if err != nil {
			return "", err
		}
		defer root.Close()
		if _, err := inspectPackageRoot(ctx, root); err != nil {
			return "", err
		}
		return "", copyPackageRoot(ctx, root, stage)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("plugin source is a symlink or special file")
	}
	data, err := readFileBounded(source, maxArchiveBytes)
	if err != nil {
		return "", err
	}
	if digest != "" {
		if err := verifyDigest(data, digest); err != nil {
			return "", err
		}
	}
	return archiveDigest(data), extractArchive(ctx, data, stage)
}

func archiveDigest(data []byte) string {
	actual := sha256.Sum256(data)
	return hex.EncodeToString(actual[:])
}

func verifyDigest(data []byte, expected string) error {
	if _, err := parseDigest(expected); err != nil {
		return err
	}
	actual := sha256.Sum256(data)
	if !strings.EqualFold(hex.EncodeToString(actual[:]), expected) {
		return fmt.Errorf("plugin ZIP sha256 does not match marketplace digest")
	}
	return nil
}
