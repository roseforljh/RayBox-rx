# Controlled uTLS Chrome 155 fork

This directory is a controlled local module copied from `github.com/metacubex/utls` v1.8.7. The module path and existing metadata/private-link compatibility are intentionally unchanged.

## Local changes

- Added the Chrome 155 ClientHello profile and retained the explicit Chrome 133 profile.
- Added the Chrome 155 `trust_anchors` extension (0xca34), 28 Chrome Root Store IDs, and JSON/dictionary registration.
- Added BoringSSL-compatible signature-algorithm GREASE seed handling while preserving the existing public GREASE helper signature.
- Added Go 1.27 standard-library ML-DSA 44/65/87 signature schemes, real TLS 1.3 CertificateVerify validation, parameter-set binding, and TLS 1.2 rejection. Client-side verification retains uTLS's supported-algorithm list (as in pinned XTLS uTLS), preserving REALITY's Ed25519 temporary-certificate handshake without changing the Chrome wire profile.
- Added independent Chrome 155 wire fixture and focused regression tests.

## Sources and compatibility

Chrome 155 template details and trust-anchor assignments are minimally adapted from XTLS/utls commit `6ebdceb2607e8c72942805af16ed4b8f052ee509`. The browser fixture and its upstream capture description were copied unchanged from pinned XTLS/utls version `v0.0.0-20261106062530-666e20f266ac`, not captured locally during this implementation. The fixture is documented in `testdata/chrome155_clienthello.md`; it contains only a public ClientHello capture. Existing uTLS source and license files remain from the metacubex baseline.

This fork requires Go 1.27 because it uses the standard `crypto/mldsa` package. No new external module dependency was added. The parent module must use a local `replace` to this directory; the parent module itself is not modified here.
