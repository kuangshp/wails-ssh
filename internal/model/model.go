package model

// Profile deliberately excludes credentials from list responses.
type Profile struct {
	ID              int64  `json:"id"`
	Name            string `json:"name"`
	Host            string `json:"host"`
	Port            int    `json:"port"`
	Username        string `json:"username"`
	AuthKind        string `json:"authKind"`
	KeyPath         string `json:"keyPath"`
	GroupName       string `json:"groupName"`
	Remark          string `json:"remark"`
	LastConnectedAt string `json:"lastConnectedAt"`
	OSID            string `json:"osId"`
	CPUCores        int    `json:"cpuCores"`
	MemoryBytes     int64  `json:"memoryBytes"`
	DiskBytes       int64  `json:"diskBytes"`
	HasSecret       bool   `json:"hasSecret"`
}

type Connection struct {
	ID        string `json:"id"`
	ProfileID int64  `json:"profileId"`
	Name      string `json:"name"`
	Home      string `json:"home"`
}

type Directory struct {
	Path    string        `json:"path"`
	Entries []RemoteEntry `json:"entries"`
}

type RemoteEntry struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	IsDir      bool   `json:"isDir"`
	IsSymlink  bool   `json:"isSymlink"`
	Size       int64  `json:"size"`
	ModifiedAt string `json:"modifiedAt"`
	Mode       string `json:"mode"`
}

type AppInfo struct {
	Version string `json:"version"`
	DataDir string `json:"dataDir"`
}
