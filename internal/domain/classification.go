package domain

// FileKind is the kind of a regular file inferred from its name (§6.2). It is
// a hint, never verified format evidence.
type FileKind string

const (
	FileKindImage      FileKind = "image"
	FileKindVideo      FileKind = "video"
	FileKindAudio      FileKind = "audio"
	FileKindDocument   FileKind = "document"
	FileKindSource     FileKind = "source"
	FileKindArchive    FileKind = "archive"
	FileKindInstaller  FileKind = "installer"
	FileKindExecutable FileKind = "executable"
	FileKindSystem     FileKind = "system"
	// FileKindOther is the kind of every file no rule recognizes.
	FileKindOther FileKind = "other"
)

// FileKinds lists every file kind in §6.2 order.
var FileKinds = []FileKind{
	FileKindImage, FileKindVideo, FileKindAudio, FileKindDocument, FileKindSource,
	FileKindArchive, FileKindInstaller, FileKindExecutable, FileKindSystem, FileKindOther,
}

// ParseFileKind validates a file kind name; an unknown one is invalid_request.
func ParseFileKind(s string) (FileKind, error) { return parseEnum("file kind", FileKinds, s) }

// Category is the classification of a folder or file, from the fixed list of
// §6.6. An entry that has not been classified has no category, which is
// distinct from CategoryUnknown.
type Category string

const (
	CategoryPersonalMedia            Category = "personal_media"
	CategoryDocuments                Category = "documents"
	CategorySourceProject            Category = "source_project"
	CategoryApplicationUserData      Category = "application_user_data"
	CategoryApplicationInstallation  Category = "application_installation"
	CategoryApplicationConfiguration Category = "application_configuration"
	CategoryOSInstallation           Category = "os_installation"
	// CategoryInstallerDownload holds installers and disk images.
	CategoryInstallerDownload  Category = "installer_download"
	CategorySystemJunk         Category = "system_junk"
	CategoryCache              Category = "cache"
	CategoryTemporaryData      Category = "temporary_data"
	CategoryGeneratedArtifacts Category = "generated_artifacts"
	CategoryDownloadCollection Category = "download_collection"
	// CategoryBackup is a copy of a whole machine or disk.
	CategoryBackup  Category = "backup"
	CategoryMixed   Category = "mixed"
	CategoryUnknown Category = "unknown"
)

// Categories lists every category in §6.6 table order.
var Categories = []Category{
	CategoryPersonalMedia, CategoryDocuments, CategorySourceProject, CategoryApplicationUserData,
	CategoryApplicationInstallation, CategoryApplicationConfiguration, CategoryOSInstallation, CategoryInstallerDownload,
	CategorySystemJunk, CategoryCache, CategoryTemporaryData, CategoryGeneratedArtifacts,
	CategoryDownloadCollection, CategoryBackup, CategoryMixed, CategoryUnknown,
}

// ParseCategory validates a category name; an unknown one is invalid_request.
func ParseCategory(s string) (Category, error) { return parseEnum("category", Categories, s) }

// Family groups the categories for charts and colors (§6.6).
type Family string

const (
	FamilyPersonal   Family = "personal"
	FamilyPrograms   Family = "programs"
	FamilyDisposable Family = "disposable"
	FamilyContainers Family = "containers"
)

// Families lists every family in §6.6 table order.
var Families = []Family{FamilyPersonal, FamilyPrograms, FamilyDisposable, FamilyContainers}

// ParseFamily validates a family name; an unknown one is invalid_request.
func ParseFamily(s string) (Family, error) { return parseEnum("family", Families, s) }

// FamilyOf returns the family of c, or "" when c is not a category.
func FamilyOf(c Category) Family {
	switch c {
	case CategoryPersonalMedia, CategoryDocuments, CategorySourceProject, CategoryApplicationUserData:
		return FamilyPersonal
	case CategoryApplicationInstallation, CategoryApplicationConfiguration, CategoryOSInstallation, CategoryInstallerDownload:
		return FamilyPrograms
	case CategorySystemJunk, CategoryCache, CategoryTemporaryData, CategoryGeneratedArtifacts:
		return FamilyDisposable
	case CategoryDownloadCollection, CategoryBackup, CategoryMixed, CategoryUnknown:
		return FamilyContainers
	default:
		return ""
	}
}

// FileFamily returns the family a file counts under in the composition of
// the folders above it (design D21): its category's family, or, when its
// category is unknown (no file rule matched) or absent, its file kind's:
// image, video, audio, document, and source are personal; installer and
// executable are programs; system is disposable; archive and other are
// containers.
func FileFamily(c Category, k FileKind) Family {
	if c != CategoryUnknown {
		if f := FamilyOf(c); f != "" {
			return f
		}
	}
	switch k {
	case FileKindImage, FileKindVideo, FileKindAudio, FileKindDocument, FileKindSource:
		return FamilyPersonal
	case FileKindInstaller, FileKindExecutable:
		return FamilyPrograms
	case FileKindSystem:
		return FamilyDisposable
	default:
		return FamilyContainers
	}
}

// Trait is an observation independent of the category (§6.6). Traits are not
// proof of ownership, recoverability, or reproducibility.
type Trait string

const (
	TraitContainsUserMaterial     Trait = "contains_user_material"
	TraitContainsCredentials      Trait = "contains_credentials"
	TraitContainsDatabase         Trait = "contains_database"
	TraitContainsVCS              Trait = "contains_vcs"
	TraitPossibleGeneratedContent Trait = "possible_generated_content"
)

// Traits lists every trait in §6.6 order.
var Traits = []Trait{
	TraitContainsUserMaterial, TraitContainsCredentials, TraitContainsDatabase,
	TraitContainsVCS, TraitPossibleGeneratedContent,
}

// ParseTrait validates a trait name; an unknown one is invalid_request.
func ParseTrait(s string) (Trait, error) { return parseEnum("trait", Traits, s) }

// Triage is the suggestion the rules or a model make for an entry (§6.6). It
// never changes a decision.
type Triage string

const (
	TriageKeep    Triage = "keep"
	TriageDiscard Triage = "discard"
	TriageReview  Triage = "review"
)

// Triages lists every triage suggestion.
var Triages = []Triage{TriageKeep, TriageDiscard, TriageReview}

// ParseTriage validates a triage name; an unknown one is invalid_request.
func ParseTriage(s string) (Triage, error) { return parseEnum("triage", Triages, s) }

// Decision is the owner's decision on an entry (§6.7). An entry's own
// decision may also be absent (""), in which case it inherits the effective
// decision of its nearest decided ancestor, else DecisionUndecided.
type Decision string

const (
	DecisionUndecided Decision = "undecided"
	DecisionKeep      Decision = "keep"
	DecisionDiscard   Decision = "discard"
	DecisionLater     Decision = "later"
)

// Decisions lists every decision.
var Decisions = []Decision{DecisionUndecided, DecisionKeep, DecisionDiscard, DecisionLater}

// ParseDecision validates a decision name; an unknown one, the empty string
// included, is invalid_request.
func ParseDecision(s string) (Decision, error) { return parseEnum("decision", Decisions, s) }

// parseEnum returns the value of values named s, or an invalid_request error
// naming what kind of value s is not.
func parseEnum[T ~string](what string, values []T, s string) (T, error) {
	for _, v := range values {
		if string(v) == s {
			return v, nil
		}
	}
	return "", Errorf(CodeInvalidRequest, "unknown %s %q", what, s)
}
