module github.com/nxyexiong/OuternetAWG-client/nas

go 1.26.0

require (
	github.com/amnezia-vpn/amneziawg-go/v3 v3.1.20260828
	github.com/nxyexiong/OuternetAWG-client/shared v0.0.0
	golang.org/x/sys v0.43.0
)

require (
	golang.org/x/crypto v0.50.0 // indirect
	golang.org/x/net v0.53.0 // indirect
	golang.zx2c4.com/wintun v0.0.0-20230126152724-0fa3db229ce2 // indirect
)

replace github.com/nxyexiong/OuternetAWG-client/shared => ../shared
