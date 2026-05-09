package policy

type RuntimePolicy struct {
	Filesystem FilesystemPolicy
	Process    ProcessPolicy
	Resources  ResourcePolicy
}

type FilesystemPolicy struct {
	ReadableRoots     []string
	WritableRoots     []string
	AllowedOperations []string
}

type ProcessPolicy struct {
	AllowedCommands []string
	NetworkAccess   bool
}

type ResourcePolicy struct {
	StopPaths     []string
	RequiredFiles []string
}

type SessionPolicy struct {
	Runtime    RuntimePolicy
	WorkingDir string
}
