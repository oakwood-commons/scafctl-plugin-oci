// Package main is the entry point for the scafctl-plugin-oci plugin.
package main

import (
	"github.com/oakwood-commons/scafctl-plugin-oci/internal/oci"

	sdkplugin "github.com/oakwood-commons/scafctl-plugin-sdk/plugin"
)

func main() {
	sdkplugin.Serve(&oci.Plugin{})
}
