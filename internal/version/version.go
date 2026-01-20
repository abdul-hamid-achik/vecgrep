package version

var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

func Full() string {
	return Version + " (" + Commit + ") " + Date
}

func Short() string {
	return Version
}
