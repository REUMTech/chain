package authz

import (
	"context"
	"crypto/x509/pkix"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/golang/protobuf/proto"

	"chain/database/raft"
	"chain/errors"
	"chain/net/http/authn"
)

var ErrNotAuthorized = errors.New("not authorized")

type Authorizer struct {
	raftDB        *raft.Service
	raftPrefix    string
	policyByRoute map[string][]string
}

func NewAuthorizer(rdb *raft.Service, prefix string, policyMap map[string][]string) *Authorizer {
	return &Authorizer{
		raftDB:        rdb,
		raftPrefix:    prefix,
		policyByRoute: policyMap,
	}
}
func (a *Authorizer) Authorize(req *http.Request) error {
	policies := a.policyByRoute[strings.TrimRight(req.RequestURI, "/")]
	if policies == nil || len(policies) == 0 {
		return errors.New("missing policy on this route")
	}

	grants, err := a.grantsByPolicies(policies)
	if err != nil {
		return errors.Wrap(err)
	}

	if !authorized(req.Context(), grants) {
		return ErrNotAuthorized
	}

	return nil
}

func authzGrants(ctx context.Context, grants []*Grant) bool {
	for _, g := range grants {
		switch g.GuardType {
		case "access_token":
			if accessTokenGuardData(g) == authn.Token(ctx) {
				return true
			}
		case "x509":
			certs := authn.X509Certs(ctx)
			if (len(certs) > 0) && equalX509Name(x509GuardData(g), certs[0].Subject) {
				return true
			}
		case "localhost":
			if authn.Localhost(ctx) {
				return true
			}
		}
	}
	return false
}

func accessTokenGuardData(grant *Grant) string {
	var v struct{ ID string }
	json.Unmarshal(grant.GuardData, &v) // ignore error, returns "" on failure
	return v.ID
}

func x509GuardData(grant *Grant) pkix.Name {
	// TODO(boymanjor): We should support the standard X.500 attributes for Subjects.
	// One idea is to map the json to a pkix.Name.
	var v struct {
		Subject struct {
			CommonName         string   `json:"cn"`
			OrganizationalUnit []string `json:"ou"`
		}
	}
	json.Unmarshal(grant.GuardData, &v)
	return pkix.Name{
		CommonName:         v.Subject.CommonName,
		OrganizationalUnit: v.Subject.OrganizationalUnit,
	}
}

func equalX509Name(a, b pkix.Name) bool {
	if a.CommonName != b.CommonName {
		return false
	}
	if len(a.OrganizationalUnit) != len(b.OrganizationalUnit) {
		return false
	}
	for i := range a.OrganizationalUnit {
		if a.OrganizationalUnit[i] != b.OrganizationalUnit[i] {
			return false
		}
	}
	return true
}

func (a *Authorizer) grantsByPolicies(policies []string) ([]*Grant, error) {
	var grants []*Grant
	for _, p := range policies {
		data := a.raftDB.Stale().Get(a.raftPrefix + p)
		if data != nil {
			grantList := new(GrantList)
			err := proto.Unmarshal(data, grantList)
			if err != nil {
				return nil, errors.Wrap(err)
			}
			grants = append(grants, grantList.GetGrants()...)
		}
	}
	return grants, nil
}