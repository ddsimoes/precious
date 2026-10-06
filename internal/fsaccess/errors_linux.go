package fsaccess

import (
	"errors"
	"syscall"

	"precious/internal/domain"
)

// errNoMount reports an absolute path that no mount-table row contains, which
// only a table without "/" can produce.
var errNoMount = errors.New("fsaccess: no mount contains the path")

// openFileOutcome classifies a failure to resolve a name observed as a regular
// file. ELOOP means the name now is a symlink (O_NOFOLLOW).
func openFileOutcome(err error) domain.AccessOutcome {
	if errors.Is(err, syscall.ELOOP) {
		return domain.OutcomeChangedDuringObservation
	}
	return outcomeFor(err)
}

// reopenOutcome classifies a failure to reopen a resolved file for reading
// through /proc/self/fd. The reopen resolves no source name, so ENOENT means
// /proc is missing, never that the file is absent.
func reopenOutcome(err error) domain.AccessOutcome {
	if errors.Is(err, syscall.ENOENT) {
		return domain.OutcomeUnavailable
	}
	return outcomeFor(err)
}
