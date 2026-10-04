package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/ShirazuNagisa/isc-core/internal/api/gen"
	"github.com/google/uuid"
)

// Public services are part of the Phecda persistence port: they are what ties a Phecda
// deployment to the DNS, reverse-proxy and certificate objects the kernel owns, and the API
// exposes them under the Phecda tag alongside projects and deployments.
//
// The kernel is the only writer. That is what makes phecda_deployments.public_service_id
// trustworthy: the reference used to point at a record the GUI kept in a file of its own, so
// the two sides could disagree and nothing could detect it.

const publicServiceColumns = `id, name, kind, domains, ddns_id, route_id, favorite, sort_order, verified_at, verified_fingerprint`

// ListPublicServices returns the collection in the user's order.
func (p *PhecdaRepository) ListPublicServices(ctx context.Context) ([]gen.PublicService, error) {
	rows, err := p.s.db.QueryContext(ctx,
		`SELECT `+publicServiceColumns+` FROM public_services ORDER BY sort_order ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list public services: %w", err)
	}
	defer rows.Close()

	out := make([]gen.PublicService, 0)
	for rows.Next() {
		var (
			id, name, kind, domains string
			ddnsID, routeID         sql.NullString
			favorite, sortOrder     int64
			verifiedAt, fingerprint sql.NullString
		)
		if err := rows.Scan(&id, &name, &kind, &domains, &ddnsID, &routeID,
			&favorite, &sortOrder, &verifiedAt, &fingerprint); err != nil {
			return nil, fmt.Errorf("scan public service: %w", err)
		}
		// Always emit favorite/order so callers never have to treat "unset" and "false/0"
		// as different things: every record the kernel returns is complete.
		fav := favorite != 0
		order := int(sortOrder)
		service := gen.PublicService{
			Id:       uuid.MustParse(id),
			Name:     name,
			Kind:     gen.PublicServiceKind(kind),
			Domains:  splitLines(domains),
			Favorite: &fav,
			Order:    &order,
		}
		if ddnsID.Valid {
			v := ddnsID.String
			service.DdnsId = &v
		}
		if routeID.Valid {
			v := routeID.String
			service.RouteId = &v
		}
		if verifiedAt.Valid && verifiedAt.String != "" {
			if parsed, err := time.Parse(time.RFC3339Nano, verifiedAt.String); err == nil {
				service.VerifiedAt = &parsed
			}
		}
		if fingerprint.Valid {
			v := fingerprint.String
			service.VerifiedFingerprint = &v
		}
		out = append(out, service)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate public services: %w", err)
	}
	return out, nil
}

// ReplacePublicServices swaps the whole collection in a single transaction.
//
// Whole-collection replacement mirrors proxy_routes, and for the same reason: the set has to
// stay self-consistent (one domain should not be claimed by two published services at once),
// and a delta API would make "check then apply" span several calls during which the state is
// observably wrong.
//
// The same commit clears deployment references to services that are gone, so the kernel never
// keeps a dangling public_service_id. Callers therefore do not have to clean up after
// themselves, and cannot forget to.
func (p *PhecdaRepository) ReplacePublicServices(ctx context.Context, services []gen.PublicService) error {
	tx, err := p.s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin public services transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM public_services`); err != nil {
		return fmt.Errorf("clear public services: %w", err)
	}

	const q = `INSERT INTO public_services (` + publicServiceColumns + `, created_at, updated_at)
	           VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`
	stamp := now()
	for _, service := range services {
		var ddnsID, routeID, verifiedAt, fingerprint any
		if service.DdnsId != nil {
			ddnsID = *service.DdnsId
		}
		if service.RouteId != nil {
			routeID = *service.RouteId
		}
		if service.VerifiedAt != nil {
			verifiedAt = formatTime(*service.VerifiedAt)
		}
		if service.VerifiedFingerprint != nil {
			fingerprint = *service.VerifiedFingerprint
		}
		favorite := 0
		if service.Favorite != nil && *service.Favorite {
			favorite = 1
		}
		order := 0
		if service.Order != nil {
			order = *service.Order
		}
		if _, err := tx.ExecContext(ctx, q,
			service.Id.String(), service.Name, string(service.Kind),
			strings.Join(service.Domains, "\n"), ddnsID, routeID,
			favorite, order, verifiedAt, fingerprint, stamp, stamp); err != nil {
			return fmt.Errorf("write public service: %w", err)
		}
	}

	if _, err := tx.ExecContext(ctx, `UPDATE phecda_deployments SET public_service_id = NULL
	           WHERE public_service_id IS NOT NULL
	             AND public_service_id NOT IN (SELECT id FROM public_services)`); err != nil {
		return fmt.Errorf("clear stale public bindings: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit public services: %w", err)
	}
	return nil
}
