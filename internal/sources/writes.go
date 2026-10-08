package sources

import (
	"context"

	"precious/internal/domain"
	"precious/internal/store"
)

// Reasons a source's writes are unavailable (r3 design D1), in the order
// WritesUnavailable checks them.
const (
	// WritesForbiddenByConfig: [sources] allow_writes is false.
	WritesForbiddenByConfig = "forbidden_by_config"
	// WritesReadOnly: the filesystem is read-only.
	WritesReadOnly = "read_only"
	// WritesNoReplaceRename: the filesystem has no rename that refuses to
	// replace an existing name.
	WritesNoReplaceRename = "no_replace_rename"
)

// WritesUnavailable returns why writes cannot be turned on for src, or ""
// when they can: forbidden by the configuration (allowWrites false), a
// read-only filesystem, or one without a no-replace rename, the first that
// applies. It reads the capabilities as last recorded, not the owner's
// permission.
func WritesUnavailable(src Source, allowWrites bool) string {
	switch {
	case !allowWrites:
		return WritesForbiddenByConfig
	case src.Caps.ReadOnly:
		return WritesReadOnly
	case !src.Caps.NoReplaceRename:
		return WritesNoReplaceRename
	}
	return ""
}

// CheckWrites reads source id inside q and returns nil when it may be
// changed now, or a *domain.Error, checked in this order: unknown_source,
// source_offline (its state is not online), writes_unavailable (see
// WritesUnavailable), and writes_disabled (the owner's permission is off).
// Planning and running commands call it in their transaction, and the
// executor in each intent transaction.
func CheckWrites(ctx context.Context, q store.Queryer, id domain.SourceID, allowWrites bool) error {
	src, err := getSource(ctx, q, id)
	if err != nil {
		return err
	}
	if src.State != StateOnline {
		return domain.Errorf(domain.CodeSourceOffline, "source %q is %s", id, src.State)
	}
	if reason := WritesUnavailable(src, allowWrites); reason != "" {
		return domain.Errorf(domain.CodeWritesUnavailable, "source %q cannot be changed: %s", id, reason)
	}
	if !src.WriteEnabled {
		return domain.Errorf(domain.CodeWritesDisabled, "changes are not allowed on source %q", id)
	}
	return nil
}
