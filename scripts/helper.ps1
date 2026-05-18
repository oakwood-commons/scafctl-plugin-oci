# Build first
task build

# Get digest of a public image
scafctl run provider oci --plugin-dir ./dist operation=digest ref=docker.io/library/alpine:latest

# Get manifest
scafctl run provider oci --plugin-dir ./dist operation=manifest ref=docker.io/library/alpine:latest

# List tags
scafctl run provider oci --plugin-dir ./dist operation=ls repository=docker.io/library/alpine

# Pull to tarball
scafctl run provider oci --plugin-dir ./dist operation=pull ref=docker.io/library/alpine:latest path=/tmp/alpine.tar

# For private registries (e.g., ghcr.io), configure auth first:
scafctl run provider oci --plugin-dir ./dist `
  --settings '{"registry":"ghcr.io","username":"your-user","password":"YOUR_TOKEN"}' `
  operation=digest ref=ghcr.io/your-org/your-repo:tag