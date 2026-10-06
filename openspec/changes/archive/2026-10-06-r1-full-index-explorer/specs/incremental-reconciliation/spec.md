## REMOVED Requirements

### Requirement: One reconciliation scope per active directory
**Reason**: Per-scope reconciliation and filesystem notifications are replaced by full rescans (precious-spec-v0.3.md §7, §16).
**Migration**: Rescan the source with `start-scan`; see `file-index`.

### Requirement: A scope is clean only after a pass that saw its latest hint
**Reason**: Per-scope reconciliation and filesystem notifications are replaced by full rescans (precious-spec-v0.3.md §7, §16).
**Migration**: Rescan the source with `start-scan`; see `file-index`.

### Requirement: Scheduled reconciliation
**Reason**: Per-scope reconciliation and filesystem notifications are replaced by full rescans (precious-spec-v0.3.md §7, §16).
**Migration**: Rescan the source with `start-scan`; see `file-index`.

### Requirement: Dispatch to the source's scan
**Reason**: Per-scope reconciliation and filesystem notifications are replaced by full rescans (precious-spec-v0.3.md §7, §16).
**Migration**: Rescan the source with `start-scan`; see `file-index`.

### Requirement: Overdue scopes after downtime
**Reason**: Per-scope reconciliation and filesystem notifications are replaced by full rescans (precious-spec-v0.3.md §7, §16).
**Migration**: Rescan the source with `start-scan`; see `file-index`.

### Requirement: Coverage gaps reconcile the active frontier
**Reason**: Per-scope reconciliation and filesystem notifications are replaced by full rescans (precious-spec-v0.3.md §7, §16).
**Migration**: Rescan the source with `start-scan`; see `file-index`.

### Requirement: Owner refresh command
**Reason**: Per-scope reconciliation and filesystem notifications are replaced by full rescans (precious-spec-v0.3.md §7, §16).
**Migration**: Rescan the source with `start-scan`; see `file-index`.

### Requirement: Notifications are low-latency hints
**Reason**: Per-scope reconciliation and filesystem notifications are replaced by full rescans (precious-spec-v0.3.md §7, §16).
**Migration**: Rescan the source with `start-scan`; see `file-index`.

### Requirement: Lost notifications open a coverage gap
**Reason**: Per-scope reconciliation and filesystem notifications are replaced by full rescans (precious-spec-v0.3.md §7, §16).
**Migration**: Rescan the source with `start-scan`; see `file-index`.

### Requirement: Watch limits degrade to polling
**Reason**: Per-scope reconciliation and filesystem notifications are replaced by full rescans (precious-spec-v0.3.md §7, §16).
**Migration**: Rescan the source with `start-scan`; see `file-index`.
