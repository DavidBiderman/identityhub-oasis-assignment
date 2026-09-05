# Not needed to run IdentityHub. `docker compose up --build` needs Docker and
# nothing else; everything below is built inside containers.
#
# This is for working *on* it -- running the test suite, linting, regenerating
# code from the schema and the OpenAPI document:
#
#   brew bundle
#   make tools     # oapi-codegen, which Homebrew does not carry
#
# `brew bundle` upgrades formulae you already have if they are out of date.
# `brew bundle install --no-upgrade` installs what is missing and leaves your
# versions alone.
#
# Docker itself is deliberately absent. Homebrew does not recognise a Docker
# installed any other way -- dragged to /Applications, or by Colima, OrbStack or
# Rancher -- so listing it here would make `brew bundle` try to install Docker
# Desktop over the top and fail, for people who already have exactly what they
# need. Installation instructions belong in the README, not in a Brewfile.

# --- To run the tests ---------------------------------------------------
brew "go"    # 1.26 or later
brew "node"  # 22 or later, for the frontend typecheck
brew "jq"    # `make token` reads the access token out of the response

# --- To regenerate or lint ----------------------------------------------
brew "sqlc"          # `make generate`: database queries from db/queries
brew "golangci-lint" # `make lint`
