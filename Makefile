TAILWIND_VERSION := v4.3.2
TAILWIND_BIN := .tools/tailwindcss

CSS_SRC := internal/webui/static/src/input.css
CSS_OUT := internal/webui/static/app.css

.PHONY: css
css: $(TAILWIND_BIN)
	$(TAILWIND_BIN) -i $(CSS_SRC) -o $(CSS_OUT) --minify

.PHONY: css-watch
css-watch: $(TAILWIND_BIN)
	$(TAILWIND_BIN) -i $(CSS_SRC) -o $(CSS_OUT) --watch

$(TAILWIND_BIN):
	@mkdir -p .tools
	@os=$$(uname -s | tr '[:upper:]' '[:lower:]'); \
	arch=$$(uname -m); \
	case "$$os-$$arch" in \
		darwin-arm64) asset=tailwindcss-macos-arm64 ;; \
		darwin-x86_64) asset=tailwindcss-macos-x64 ;; \
		linux-x86_64) asset=tailwindcss-linux-x64 ;; \
		linux-aarch64) asset=tailwindcss-linux-arm64 ;; \
		*) echo "unsupported platform $$os-$$arch for standalone tailwindcss" >&2; exit 1 ;; \
	esac; \
	echo "downloading tailwindcss $(TAILWIND_VERSION) ($$asset)"; \
	curl -sL -o $(TAILWIND_BIN) "https://github.com/tailwindlabs/tailwindcss/releases/download/$(TAILWIND_VERSION)/$$asset"; \
	chmod +x $(TAILWIND_BIN)
