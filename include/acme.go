//go:build with_acme

package include

import (
	"github.com/roseforljh/RayBox-rx/adapter/certificate"
	"github.com/roseforljh/RayBox-rx/service/acme"
)

func registerACMECertificateProvider(registry *certificate.Registry) {
	acme.RegisterCertificateProvider(registry)
}
