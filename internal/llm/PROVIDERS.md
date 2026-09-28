# Provider registry and generated catalogs

`providers.go` is the source of truth for built-in providers. The generator reads `llm.ListProviders()` and produces the catalogs used by the extensions:

| Generated file | Consumer and purpose |
| --- | --- |
| `extensions/frontend/src/shared/providers.generated.ts` | Shared VS Code and IDEA configuration UI: provider metadata and ordered model presets. |
| `extensions/idea/src/main/kotlin/com/alibaba/opencodereview/idea/services/ProviderNames.generated.kt` | IDEA host: built-in provider names used to route saved configuration to `providers` instead of `custom_providers`. |

The Kotlin host needs only provider names; the full metadata remains in the shared frontend catalog. Both files are checked in, so extension builds do not require Go. Neither generated file should be edited by hand.

## Updating a provider

1. Edit the registry in `internal/llm/providers.go`. Keep model names unique within each provider and retain the intended order: the first model supplies the frontend fallback when no model is saved. Different providers may offer the same model.
2. For a new or changed provider entry, update its Go tests and the provider documentation in `pages/src/content/docs/{en,zh,ja,ru}/configuration.md`.
3. From the repository root, generate both catalogs with one command:

   ```sh
   go generate ./internal/llm
   ```

4. Review and commit both generated files alongside the registry change. Adding or renaming a provider no longer requires editing a separate Kotlin name list.

The generator reads built-in metadata only. It does not load user credentials or discover models from provider APIs. The generated `envVar` value is an environment-variable name, not its contents. Updating a catalog does not rewrite saved user configuration.

## Verifying the catalogs

Run this read-only check from the repository root:

```sh
go run ./internal/llm/gen -check -output extensions/frontend/src/shared/providers.generated.ts -kotlin-output extensions/idea/src/main/kotlin/com/alibaba/opencodereview/idea/services/ProviderNames.generated.kt
```

CI and the Go artifact test compare the existing files directly with `llm.ListProviders()`. The frontend check compares provider names and order, model order, and metadata. The Kotlin check compares the generated provider names and order. Missing files, invalid payloads, and data differences fail without creating or rewriting either file. The IDEA tests additionally exercise the compiled Kotlin names and compare them with the shared frontend catalog.

Run `make check`, `make test`, and `make coverage` after changes. The frontend and IDEA suites validate how the generated data is consumed.

## Troubleshooting

- **Duplicate model:** generation and verification reject the registry with an error such as `provider "example" has duplicate model "model-name"`. Remove the duplicate at the source. The generator does not silently deduplicate or reorder models, and the frontend tests retain their existing uniqueness expectation.
- **Missing or stale artifact:** run `go generate ./internal/llm` from the repository root, inspect the changes, and commit both generated files. Re-running CI does not regenerate files for you.
- **Output-path error:** the generator requires both `-output` and `-kotlin-output`. The `go:generate` directive supplies the repository paths; use the command above unless testing output in another location.
