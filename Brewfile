# Everything needed to develop and run IdentityHub on a fresh macOS machine.
#
#   brew bundle
#
# Then `make deploy` brings the stack up, or `make help` lists everything else.

# --- Running the stack --------------------------------------------------
# Everything else is built inside containers, so this is the only hard
# requirement for `make deploy`.
cask "docker"

# --- Backend ------------------------------------------------------------
brew "go"           # 1.26 or later; the Dockerfile pins the build toolchain
brew "sqlc"         # regenerates database queries from db/queries
brew "golangci-lint" # `make lint`

# --- Frontend -----------------------------------------------------------
brew "node"         # 22 or later, for the Vite build and typecheck

# --- Working with the running stack -------------------------------------
brew "temporal"     # inspect workflows and schedules from the terminal
brew "libpq"        # psql, for looking at the database directly
brew "jq"           # formats API responses in the examples throughout the docs
