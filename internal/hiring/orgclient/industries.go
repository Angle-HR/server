package orgclient

import (
	"context"
	"sync"
	"time"

	orgv1 "github.com/Angle-HR/server/gen/openhr/org/v1"
	"github.com/Angle-HR/server/internal/hiring/draft"
	"github.com/Angle-HR/server/internal/hiring/hiringtypes"
)

// DefaultIndustryTTL is how long the org industry catalog is kept: it changes rarely.
const DefaultIndustryTTL = 10 * time.Minute

// Industries serves the industry catalog from OrgService.ListIndustries with a long TTL cache, and checks a
// job's industry id against it. Everything else of draft.Reference is delegated to Base (the global database).
//
// Industry validation is not a compliance gate, so it degrades instead of failing closed: when org cannot be
// reached the stale cache is used, and with no cache at all the Base (database) answer is. Drafting keeps working.
type Industries struct {
	draft.Reference // Base: markets, skills, seniority, gates, registry...
	Client          OrgClient
	TTL             time.Duration
	Timeout         time.Duration
	// Now is for tests.
	Now func() time.Time

	mu      sync.Mutex
	items   []hiringtypes.CatalogItem
	fetched time.Time
}

func (i *Industries) now() time.Time {
	if i.Now != nil {
		return i.Now()
	}
	return time.Now()
}

func (i *Industries) list(ctx context.Context) ([]hiringtypes.CatalogItem, bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	ttl := i.TTL
	if ttl <= 0 {
		ttl = DefaultIndustryTTL
	}
	if i.items != nil && i.now().Sub(i.fetched) < ttl {
		return i.items, true
	}
	cctx, cancel := context.WithTimeout(ctx, orDefault(i.Timeout))
	defer cancel()
	resp, err := i.Client.ListIndustries(cctx, &orgv1.ListIndustriesRequest{})
	if err != nil {
		return i.items, i.items != nil // stale beats nothing
	}
	out := make([]hiringtypes.CatalogItem, 0, len(resp.GetIndustries()))
	for _, in := range resp.GetIndustries() {
		out = append(out, hiringtypes.CatalogItem{ID: in.GetId(), Slug: in.GetSlug(), Name: in.GetName()})
	}
	i.items, i.fetched = out, i.now()
	return out, true
}

// Industries implements draft.Reference.
func (i *Industries) Industries(ctx context.Context) ([]hiringtypes.CatalogItem, error) {
	if items, ok := i.list(ctx); ok {
		return items, nil
	}
	return i.Reference.Industries(ctx)
}

// CheckRefs implements draft.Reference: the database checks everything but the industry, which org answers.
func (i *Industries) CheckRefs(
	ctx context.Context, skillIDs []string, industry, seniority, experience string,
) (*hiringtypes.RefCheck, error) {
	items, ok := i.list(ctx)
	if !ok || industry == "" {
		return i.Reference.CheckRefs(ctx, skillIDs, industry, seniority, experience)
	}
	rc, err := i.Reference.CheckRefs(ctx, skillIDs, "", seniority, experience)
	if err != nil {
		return nil, err
	}
	rc.IndustryOK = false
	for _, it := range items {
		if it.ID == industry {
			rc.IndustryOK = true
			break
		}
	}
	return rc, nil
}

var _ draft.Reference = (*Industries)(nil)
