package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

//go:generate go run github.com/josephspurrier/goversioninfo/cmd/goversioninfo@v1.5.0 -platform-specific -propagate-ver-strings

//go:embed versioninfo.json
var versionInfoJSON []byte

func printVersion() error {
	var info struct {
		StringFileInfo struct {
			ProductName    string
			ProductVersion string
			CompanyName    string
		}
	}
	if err := json.Unmarshal(versionInfoJSON, &info); err != nil {
		return fmt.Errorf("read application version: %w", err)
	}
	fmt.Printf("%s v%s\nAuthor: %s\n", info.StringFileInfo.ProductName, info.StringFileInfo.ProductVersion, info.StringFileInfo.CompanyName)
	return nil
}
