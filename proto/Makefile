# Makefile контракта brigade. proto/ — источник истины API; сгенерированный код
# (backend/gen/go, web/src/api/gen) производный и руками не правится.

# Кодген TS-плагинами buf требует node_modules/.bin web в PATH; Go-плагины — в $GOPATH/bin.
# TS-плагины (protoc-gen-es, protoc-gen-connect-es) подтягиваются из web/node_modules
# после `npm install` в web/. Go-плагины (protoc-gen-go, protoc-gen-connect-go) ставятся
# в $GOPATH/bin — см. proto/buf.gen.yaml.
WEB_BIN := ../web/node_modules/.bin

.PHONY: gen lint

# Кодген Go+TS из proto через buf.
gen:
	PATH="$(WEB_BIN):$$(go env GOPATH)/bin:$$PATH" buf generate

# Линт контракта.
lint:
	buf lint
