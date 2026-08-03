// Package exitcodes exposes the corekit CLI error contract at the application boundary.
package exitcodes

import corekit "github.com/danieljustus/symaira-corekit/exitcodes"

type ExitCode = corekit.ExitCode
type ErrorKind = corekit.ErrorKind
type CLIError = corekit.CLIError

const (
	ExitOK          = corekit.ExitOK
	ExitGeneric     = corekit.ExitGeneric
	ExitNoInput     = corekit.ExitNoInput
	ExitNoAuth      = corekit.ExitNoAuth
	ExitForbidden   = corekit.ExitForbidden
	ExitNotFound    = corekit.ExitNotFound
	ExitConflict    = corekit.ExitConflict
	ExitSoftware    = corekit.ExitSoftware
	ExitData        = corekit.ExitData
	ExitConfig      = corekit.ExitConfig
	ExitInterrupted = corekit.ExitInterrupted

	KindNotFound    = corekit.KindNotFound
	KindAuth        = corekit.KindAuth
	KindPermission  = corekit.KindPermission
	KindValidation  = corekit.KindValidation
	KindConfig      = corekit.KindConfig
	KindConflict    = corekit.KindConflict
	KindInternal    = corekit.KindInternal
	KindUnavailable = corekit.KindUnavailable
)

var (
	ExitCodeFromError = corekit.ExitCodeFromError
	FormatCLIError    = corekit.FormatCLIError
	Wrap              = corekit.Wrap
	Wrapf             = corekit.Wrapf
)
