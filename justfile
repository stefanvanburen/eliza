# https://just.systems

# Run lint and test.
@default: lint test

# Run tests with race detector enabled.
test:
    go test -race ./...

# Run linters (go vet and staticcheck).
#
# -printf.funcs names ok.Sprintf so vet checks the format strings inside
# test assertion options; printf's default function list omits it, and go
# test's built-in vet cannot be passed analyzer flags.
lint:
    go vet -printf.funcs=Sprintf ./...
    go tool honnef.co/go/tools/cmd/staticcheck ./...
    go fix -diff ./...
    test -z "$(gofmt -l .)" || (echo "gofmt needed on:"; gofmt -l .; exit 1)
