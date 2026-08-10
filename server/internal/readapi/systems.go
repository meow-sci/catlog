package readapi

import (
	"context"
	"database/sql"
	"net/http"

	"github.com/meow-sci/catlog/server/internal/store"
)

// SystemsResponse is GET /v1/systems.
type SystemsResponse struct {
	Systems []SystemSummary `json:"systems"`
}

// SystemSummary is one celestial-system header and its visible community.
type SystemSummary struct {
	Hash     string `json:"hash"`
	SystemID string `json:"system_id"`
	Name     string `json:"name"`
	Slug     string `json:"slug"`
	HomeBody string `json:"home_body"`
	Bodies   int64  `json:"bodies"`
	Complete bool   `json:"complete"`
	Players  int64  `json:"players"`
	Careers  int64  `json:"careers"`
}

// SystemDetail is GET /v1/systems/{slug}. The route also accepts a raw hash.
type SystemDetail struct {
	Hash     string       `json:"hash"`
	SystemID string       `json:"system_id"`
	Name     string       `json:"name"`
	Slug     string       `json:"slug"`
	HomeBody string       `json:"home_body"`
	Roots    []string     `json:"roots"`
	Players  int64        `json:"players"`
	Careers  int64        `json:"careers"`
	Complete bool         `json:"complete"`
	Bodies   []SystemBody `json:"bodies"`
}

// Vector3 is a Cartesian vector in the body-centred ecliptic frame.
type Vector3 struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z"`
}

// Quaternion is a body-fixed-to-ecliptic orientation.
type Quaternion struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z"`
	W float64 `json:"w"`
}

// SystemBody is one immutable catalogue body. Orbital shape is absent on a
// root; period is independently absent for an unbound conic.
type SystemBody struct {
	Body       string     `json:"body"`
	Name       string     `json:"name"`
	Class      string     `json:"class"`
	Kind       string     `json:"kind"`
	Rank       int64      `json:"rank"`
	Parent     *string    `json:"parent,omitempty"`
	RadiusM    float64    `json:"radius_m"`
	MassKg     float64    `json:"mass_kg"`
	SoiM       float64    `json:"soi_m"`
	AtmoM      float64    `json:"atmo_m"`
	OceanM     float64    `json:"ocean_m"`
	AngVel     float64    `json:"angvel"`
	Axis       Vector3    `json:"axis"`
	SmaM       *float64   `json:"sma_m,omitempty"`
	Ecc        *float64   `json:"ecc,omitempty"`
	IncDeg     *float64   `json:"inc_deg,omitempty"`
	LanDeg     *float64   `json:"lan_deg,omitempty"`
	ArgpDeg    *float64   `json:"argp_deg,omitempty"`
	TPe        *float64   `json:"t_pe,omitempty"`
	PeriodS    *float64   `json:"period_s,omitempty"`
	CcfToCceT0 Quaternion `json:"ccf_to_cce_t0"`
}

type systemCounts struct {
	players int64
	careers int64
}

func (s *Server) handleSystems(w http.ResponseWriter, r *http.Request) {
	out, err := s.Systems(r.Context())
	if err != nil {
		s.fail(w, r, err, "read systems")
		return
	}
	s.writeJSON(w, http.StatusOK, out)
}

// Systems assembles GET /v1/systems in first-seen order.
func (s *Server) Systems(ctx context.Context) (SystemsResponse, error) {
	var headers []store.SystemRow
	var careerCounts []store.SystemCareerCount
	err := s.deps.Projections.With(func(p *store.Projections) error {
		var err error
		if headers, err = p.Systems(ctx); err != nil {
			return err
		}
		careerCounts, err = p.SystemCareerCounts(ctx, "")
		return err
	})
	if err != nil {
		return SystemsResponse{}, err
	}
	counts := s.visibleSystemCounts(careerCounts)
	out := SystemsResponse{Systems: make([]SystemSummary, 0, len(headers))}
	for _, h := range headers {
		c := counts[h.Hash]
		out.Systems = append(out.Systems, SystemSummary{
			Hash: h.Hash, SystemID: h.SystemID, Name: h.Name, Slug: h.Slug,
			HomeBody: h.HomeBody, Bodies: h.BodyCount, Complete: h.Complete,
			Players: c.players, Careers: c.careers,
		})
	}
	return out, nil
}

func (s *Server) handleSystem(w http.ResponseWriter, r *http.Request) {
	out, ok, err := s.System(r.Context(), r.PathValue("slug"))
	if err != nil {
		s.fail(w, r, err, "read system")
		return
	}
	if !ok {
		s.writeError(w, http.StatusNotFound, "not_found", "system not found")
		return
	}
	s.writeJSON(w, http.StatusOK, out)
}

// System assembles one complete, unpaged catalogue resolved by slug or hash.
func (s *Server) System(ctx context.Context, key string) (SystemDetail, bool, error) {
	var header store.SystemRow
	var bodies []store.SystemBodyRow
	var careerCounts []store.SystemCareerCount
	var ok bool
	err := s.deps.Projections.With(func(p *store.Projections) error {
		var err error
		header, ok, err = p.SystemBySlugOrHash(ctx, key)
		if err != nil || !ok {
			return err
		}
		if bodies, err = p.SystemBodies(ctx, header.Hash); err != nil {
			return err
		}
		careerCounts, err = p.SystemCareerCounts(ctx, header.Hash)
		return err
	})
	if err != nil || !ok {
		return SystemDetail{}, ok, err
	}
	counts := s.visibleSystemCounts(careerCounts)[header.Hash]
	out := SystemDetail{
		Hash: header.Hash, SystemID: header.SystemID, Name: header.Name, Slug: header.Slug,
		HomeBody: header.HomeBody, Roots: make([]string, 0), Players: counts.players,
		Careers: counts.careers, Complete: header.Complete, Bodies: make([]SystemBody, 0, len(bodies)),
	}
	for _, body := range bodies {
		if !body.Parent.Valid {
			out.Roots = append(out.Roots, body.Body)
		}
		out.Bodies = append(out.Bodies, responseSystemBody(body))
	}
	return out, true, nil
}

func (s *Server) visibleSystemCounts(rows []store.SystemCareerCount) map[string]systemCounts {
	out := make(map[string]systemCounts)
	for _, row := range rows {
		if _, visible := s.deps.Directory.Handle(row.PlayerID); !visible {
			continue
		}
		c := out[row.Hash]
		c.players++
		c.careers += row.Careers
		out[row.Hash] = c
	}
	return out
}

func responseSystemBody(row store.SystemBodyRow) SystemBody {
	return SystemBody{
		Body: row.Body, Name: row.Name, Class: row.Class, Kind: row.Kind, Rank: row.Rank,
		Parent: nullableStringPointer(row.Parent), RadiusM: row.RadiusM, MassKg: row.MassKg,
		SoiM: row.SoiM, AtmoM: row.AtmoM, OceanM: row.OceanM, AngVel: row.AngVel,
		Axis: Vector3{X: row.AxisX, Y: row.AxisY, Z: row.AxisZ},
		SmaM: nullableFloatPointer(row.SmaM), Ecc: nullableFloatPointer(row.Ecc),
		IncDeg: nullableFloatPointer(row.IncDeg), LanDeg: nullableFloatPointer(row.LanDeg),
		ArgpDeg: nullableFloatPointer(row.ArgpDeg), TPe: nullableFloatPointer(row.TPe),
		PeriodS:    nullableFloatPointer(row.PeriodS),
		CcfToCceT0: Quaternion{X: row.QuatX, Y: row.QuatY, Z: row.QuatZ, W: row.QuatW},
	}
}

func nullableStringPointer(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	return &v.String
}

func nullableFloatPointer(v sql.NullFloat64) *float64 {
	if !v.Valid {
		return nil
	}
	return &v.Float64
}
