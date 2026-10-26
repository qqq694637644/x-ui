module x-ui

go 1.26.0

toolchain go1.26.8

require (
	github.com/BurntSushi/toml v0.3.1
	github.com/Workiva/go-datastructures v1.0.53
	github.com/apernet/quic-go v0.63.1-0.20261004180939-a10df75c260c
	github.com/fatedier/frp v0.62.2
	github.com/gin-contrib/sessions v0.0.3
	github.com/gin-gonic/gin v1.7.1
	github.com/go-telegram-bot-api/telegram-bot-api/v5 v5.5.1
	github.com/nicksnyder/go-i18n/v2 v2.1.2
	github.com/op/go-logging v0.0.0-20160315200505-970db520ece7
	github.com/robfig/cron/v3 v3.0.1
	github.com/shirou/gopsutil v3.21.3+incompatible
	github.com/xtls/xray-core v1.4.2
	go.uber.org/atomic v1.7.0
	golang.org/x/text v0.41.0
	google.golang.org/grpc v1.55.0
	gorm.io/driver/sqlite v1.1.4
	gorm.io/gorm v1.21.9
)

// Use the exact audited bounded-Brutal sender already used by the custom frpc.
replace github.com/fatedier/frp => github.com/qqq694637644/frp v0.62.2-0.20261020141929-7eb28f6ca380

require (
	github.com/StackExchange/wmi v0.0.0-20210224194228-fe8f1750fd46 // indirect
	github.com/andybalholm/brotli v1.0.6 // indirect
	github.com/gin-contrib/sse v0.1.0 // indirect
	github.com/go-ole/go-ole v1.2.5 // indirect
	github.com/go-playground/locales v0.13.0 // indirect
	github.com/go-playground/universal-translator v0.17.0 // indirect
	github.com/go-playground/validator/v10 v10.4.1 // indirect
	github.com/golang/mock v1.6.0 // indirect
	github.com/golang/protobuf v1.5.3 // indirect
	github.com/gorilla/context v1.1.1 // indirect
	github.com/gorilla/securecookie v1.1.1 // indirect
	github.com/gorilla/sessions v1.1.3 // indirect
	github.com/jinzhu/inflection v1.0.0 // indirect
	github.com/jinzhu/now v1.1.2 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/klauspost/compress v1.17.4 // indirect
	github.com/kr/pretty v0.3.1 // indirect
	github.com/leodido/go-urn v1.2.0 // indirect
	github.com/mattn/go-isatty v0.0.16 // indirect
	github.com/mattn/go-sqlite3 v1.14.14 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.2 // indirect
	github.com/pires/go-proxyproto v0.15.0 // indirect
	github.com/refraction-networking/utls v1.8.2 // indirect
	github.com/rogpeppe/go-internal v1.10.0 // indirect
	github.com/tklauser/go-sysconf v0.3.5 // indirect
	github.com/tklauser/numcpus v0.2.2 // indirect
	github.com/ugorji/go/codec v1.1.7 // indirect
	golang.org/x/crypto v0.56.0 // indirect
	golang.org/x/net v0.57.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20230530153820-e85fd2cbaebc // indirect
	google.golang.org/protobuf v1.36.5 // indirect
	gopkg.in/check.v1 v1.0.0-20201130134442-10cb98267c6c // indirect
	gopkg.in/yaml.v2 v2.4.0 // indirect
)
