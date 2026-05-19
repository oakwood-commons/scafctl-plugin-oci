version: "2"

linters:
  enable:
    - errcheck
    - govet
    - ineffassign
    - staticcheck
    - unused
    - revive
    - gosec
    - misspell
  exclusions:
    rules:
      - path: _test\.go
        text: "G101:"
        linters:
          - gosec
