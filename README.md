# spvbit

SPV Bitcoin wallet with a [Fyne](https://fyne.io) GUI, written in Go.

Current milestone: wallet bootstrap —

- **HD wallet keys** generated from a BIP39 mnemonic (BIP84, native SegWit),
- the current **P2WPKH (bech32) receive address** shown as a **QR code in the
  centre of the window** with the address text below it,
- keys, xpub and derived addresses stored in **SQLite built with SQLCipher**
  (encrypted storage, cgo-based driver),
- `Copy Address` and `New Address` actions (derives the next index on
  `m/84'/coin'/0'/0/i`).

## Build

```sh
go build -o spvbit ./cmd/spvbit
```

Requires Go with cgo enabled and a C toolchain (Xcode CLT on macOS).
The SQLCipher dependency compiles the bundled amalgamation + libtomcrypt,
so no system SQLCipher/OpenSSL is needed.

## Run

```sh
./spvbit -net testnet                       # GUI, default datadir ~/.spvbit
./spvbit -datadir ./data -net mainnet       # custom datadir
./spvbit -dbpass 'your-passphrase'          # encrypted wallet database
./spvbit -check                             # headless smoke check, prints address + writes QR png
```

Flags:

| flag       | default      | meaning                                          |
|------------|--------------|--------------------------------------------------|
| `-datadir` | `~/.spvbit`  | directory holding `spvbit.db`                    |
| `-net`     | `mainnet`    | `mainnet`, `testnet`, `regtest` or `simnet`      |
| `-dbpass`  | *(empty)*    | SQLCipher passphrase; empty = unencrypted DB     |
| `-check`   | `false`      | init wallet, print address, exit (no GUI)        |

## Security / encryption notes

- The driver is
  [mutecomm/go-sqlcipher](https://github.com/mutecomm/go-sqlcipher) (SQLCipher
  compiled with libtomcrypt — self-contained, no external crypto library).
  It is chosen deliberately so the wallet file can be **encrypted with a
  passphrase**, and it will be: currently the passphrase comes from `-dbpass`;
  a later milestone wires it to the OS keychain/secret store.
- With `-dbpass` set, the file on disk is a SQLCipher database (no plaintext
  SQLite header, pages encrypted). The mnemonic is stored inside it.
- Without `-dbpass` the database is plain SQLite — dev convenience only.

## Layout

```
cmd/spvbit/main.go     CLI flags, headless -check mode
internal/wallet        BIP39/BIP84 HD derivation, P2WPKH addresses
internal/storage       SQLCipher SQLite store (meta + addresses)
internal/gui           Fyne window, QR widget, address display
```

## Tests

```sh
go test ./...
```

Includes the official BIP84 test vector (mnemonic `abandon ... about` →
`bc1qcr8te4kr609gcawutmrza0j4xv80jy8z306fyu`) and encrypted-DB round-trip
tests.

## CI & linters

[`.github/workflows/ci.yml`](.github/workflows/ci.yml) runs on pushes to
`main` and on every pull request: build, plain `go vet`, the four
[govets](https://github.com/pin2t/govets) linters (pinned to `v1.2.0`),
gofmt and the test suite.

The govets linters enforce a strict style that the codebase follows:

- `imports-vet` — every import has its own `import` keyword and line, with no
  blank lines between imports;
- `funcbody-vet` — no comments and no blank lines inside function bodies;
  explanations live in doc comments above the function;
- `vardecl-vet` — `var x = f()` instead of `x := f()` wherever Go allows it;
- `varname-vet` — function-body variables have at most two camel-case words.

To run them locally:

```sh
go install github.com/pin2t/govets/imports-vet@v1.2.0 github.com/pin2t/govets/funcbody-vet@v1.2.0 github.com/pin2t/govets/vardecl-vet@v1.2.0 github.com/pin2t/govets/varname-vet@v1.2.0
go vet -vettool="$(go env GOPATH)/bin/imports-vet" ./...
go vet -vettool="$(go env GOPATH)/bin/funcbody-vet" ./...
go vet -vettool="$(go env GOPATH)/bin/vardecl-vet" ./...
go vet -vettool="$(go env GOPATH)/bin/varname-vet" ./...
```

Keep running plain `go vet ./...` as well: `-vettool` replaces, not adds to,
the built-in analyzers.

## Roadmap

- [x] HD key generation, SegWit addresses, QR display, encrypted SQLite
- [ ] SPV sync: header chain download + BIP158 compact block filters
- [ ] Bloom/utxo tracking of wallet addresses, balance and history UI
- [ ] Spend path: PSBT creation/signing (hardware wallet friendly)
- [ ] Keyring integration for the database passphrase
