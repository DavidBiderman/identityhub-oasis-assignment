# Optional. `make deploy` needs Docker and nothing else.
#
#   brew bundle
#
# Two things worth knowing before running it:
#
#   - If Docker is already installed but not through Homebrew -- dragged to
#     /Applications, or installed by Colima, OrbStack or Rancher -- brew does
#     not recognise it and will try to install Docker Desktop over the top,
#     which fails rather than silently reinstalling. Comment out the cask
#     below, or run `brew bundle install --no-lock` after removing that line.
#
#   - `brew bundle` upgrades formulae that are already installed but out of
#     date. `brew bundle install --no-upgrade` installs what is missing and
#     leaves your existing versions alone.

# --- The only hard requirement ------------------------------------------
# Everything else is built and run inside containers.
#
# Any Docker with Compose v2 works; this cask is one convenient way to get
# one, not a requirement in itself.
cask "docker-desktop"

# --- To run the tests ---------------------------------------------------
brew "go"    # 1.26 or later
brew "node"  # 22 or later, for the frontend typecheck
brew "jq"    # `make token` reads the access token out of the response

# --- Only to regenerate or lint -----------------------------------------
# Neither is needed to run the project or its tests.
brew "sqlc"          # `make generate`: database queries from db/queries
brew "golangci-lint" # `make lint`

# `make generate` also needs oapi-codegen, which Homebrew does not carry:
#
#   go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@latest
#
# or `make tools`, which installs it into $(go env GOPATH)/bin.
