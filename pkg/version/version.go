package version

import (
	"encoding/json"
	"fmt"
	"os"
)

var (
	Version   = "dev"
	Commit    = ""
	BuildDate = ""
	BuiltBy   = ""
	Info      BuildInfo
)

func init() {
	Info = BuildInfo{
		Version:  Version,
		Commit:   Commit,
		Date:     BuildDate,
		BuildtBy: BuiltBy,
	}
}

type BuildInfo struct {
	Version  string `json:"version"`
	Commit   string `json:"commit"`
	Date     string `json:"build_date"`
	BuildtBy string `json:"buildt_by"`
}

func (i BuildInfo) Print(output string) {
	switch output {
	case "json":
		res, err := json.Marshal(Info)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: could not marshal version info to json %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(res))

	default:
		fmt.Printf(`distribrr
Version: %s
Commit: %s
Date: %s
Built by: %s
`, Version, Commit, BuildDate, BuiltBy)
	}
}
