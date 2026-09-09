// Package clients holds the BFF's gRPC connections to the domain services.
package clients

import (
	"google.golang.org/grpc"

	catalogv1 "github.com/deeprath/commerce-platform/gen/go/commerce/catalog/v1"
	mediav1 "github.com/deeprath/commerce-platform/gen/go/commerce/media/v1"
	searchv1 "github.com/deeprath/commerce-platform/gen/go/commerce/search/v1"
	"github.com/deeprath/commerce-platform/pkg/grpcx"
)

type Set struct {
	Catalog catalogv1.CatalogServiceClient
	Media   mediav1.MediaServiceClient
	Search  searchv1.SearchServiceClient

	conns []*grpc.ClientConn
}

// Dial opens connections to catalog, media and search at the given targets.
func Dial(catalogAddr, mediaAddr, searchAddr string) (*Set, error) {
	cc, err := grpcx.Dial(catalogAddr)
	if err != nil {
		return nil, err
	}
	mc, err := grpcx.Dial(mediaAddr)
	if err != nil {
		_ = cc.Close()
		return nil, err
	}
	sc, err := grpcx.Dial(searchAddr)
	if err != nil {
		_ = cc.Close()
		_ = mc.Close()
		return nil, err
	}
	return &Set{
		Catalog: catalogv1.NewCatalogServiceClient(cc),
		Media:   mediav1.NewMediaServiceClient(mc),
		Search:  searchv1.NewSearchServiceClient(sc),
		conns:   []*grpc.ClientConn{cc, mc, sc},
	}, nil
}

func (s *Set) Close() {
	for _, c := range s.conns {
		_ = c.Close()
	}
}
