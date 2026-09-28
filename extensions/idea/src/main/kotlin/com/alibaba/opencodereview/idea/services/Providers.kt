// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package com.alibaba.opencodereview.idea.services

import java.util.Locale

// The host needs only names to route configuration to providers or custom_providers.
private val PRESET_PROVIDER_NAMES: Set<String> = generatedPresetProviderNames()

/** Compares after trim + lowercase; Locale.ROOT avoids surprises such as the Turkish-locale 'I' lowercasing to a dotless i. */
fun isPresetProvider(name: String): Boolean =
    PRESET_PROVIDER_NAMES.contains(name.trim().lowercase(Locale.ROOT))

/** Read-only view for tests and diagnostics. Returns a defensive copy so callers cannot mutate the backing set. */
fun presetProviderNames(): Set<String> = PRESET_PROVIDER_NAMES.toSet()
