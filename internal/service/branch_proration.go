package service

import (
	"clientesFrecuentes/internal/model"
	"clientesFrecuentes/internal/repository"
	"context"
	"github.com/google/uuid"
	"log/slog"
	"math"
	"strings"
	"time"
)

// CalendarProration uses civil dates, never 24-hour durations or floating point money.
func CalendarProration(now, next time.Time, unit int64, trial *time.Time) (remaining, days int, amount int64, start, end time.Time, err error) {
	return anchoredCalendarProration(now, next, unit, trial, next.In(argentinaLocation()).Day())
}
func argentinaLocation() *time.Location {
	loc, _ := time.LoadLocation("America/Argentina/Buenos_Aires")
	return loc
}
func anchoredCalendarProration(now, next time.Time, unit int64, trial *time.Time, anchor int) (remaining, days int, amount int64, start, end time.Time, err error) {
	loc, e := time.LoadLocation("America/Argentina/Buenos_Aires")
	if e != nil {
		err = e
		return
	}
	civil := func(t time.Time) time.Time {
		t = t.In(loc)
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
	}
	start = civil(now)
	end = civil(next)
	if anchor < 1 || anchor > 31 {
		err = repository.ErrQuoteChanged
		return
	}
	if end.Equal(start) {
		end = anchoredMonth(end, 1, anchor)
	}
	if end.Before(start) || unit < 0 || unit > math.MaxInt64/62 {
		err = repository.ErrQuoteChanged
		return
	}
	if !end.Equal(anchoredMonth(end, 0, anchor)) {
		err = repository.ErrQuoteChanged
		return
	}
	cycleStart := anchoredMonth(end, -1, anchor)
	if start.Before(cycleStart) {
		err = repository.ErrQuoteChanged
		return
	}
	count := func(a, b time.Time) int {
		n := 0
		for a.Before(b) {
			a = a.AddDate(0, 0, 1)
			n++
		}
		return n
	}
	days = count(cycleStart, end)
	remaining = count(start, end)
	if days < 1 || remaining > days {
		err = repository.ErrQuoteChanged
		return
	}
	if trial == nil || !trial.After(now) {
		amount = (unit*int64(remaining) + int64(days)/2) / int64(days)
	}
	return
}
func shiftMonth(t time.Time, n int) time.Time { return anchoredMonth(t, n, t.Day()) }
func anchoredMonth(t time.Time, n, anchor int) time.Time {
	first := time.Date(t.Year(), t.Month()+time.Month(n), 1, 0, 0, 0, 0, t.Location())
	last := first.AddDate(0, 1, -1).Day()
	day := anchor
	if day > last {
		day = last
	}
	return time.Date(first.Year(), first.Month(), day, 0, 0, 0, 0, t.Location())
}
func (s *Service) branchProrationAvailable() bool {
	return s.Config.BranchProrationEnabled && (s.Config.BranchPaymentSimulator || s.Billing != nil)
}
func (s *Service) QuoteBranch(ctx context.Context, actor, brand int64, in model.CreateBranchRequest) (model.BranchQuote, error) {
	if !s.branchProrationAvailable() {
		return model.BranchQuote{}, ErrBillingUnavailable
	}
	if e := cleanBranch(&in); e != nil {
		return model.BranchQuote{}, e
	}
	now := s.Now()
	return s.Repo.SaveBranchQuote(ctx, actor, brand, in, func(snapshot repository.BranchSnapshot) (model.BranchQuote, error) {
		remaining, days, amount, start, end, e := anchoredCalendarProration(now, snapshot.Next, snapshot.Unit, snapshot.Trial, snapshot.Anchor)
		if e != nil {
			return model.BranchQuote{}, e
		}
		expiry := now.Add(10 * time.Minute)
		midnight := start.AddDate(0, 0, 1)
		if expiry.After(midnight) {
			expiry = midnight
		}
		if snapshot.Monthly > math.MaxInt64-snapshot.Unit {
			return model.BranchQuote{}, ErrInvalidRequest
		}
		return model.BranchQuote{ExpiresAt: expiry, Currency: "ARS", CurrentMonthlyAmountCents: snapshot.Monthly, NewMonthlyAmountCents: snapshot.Monthly + snapshot.Unit, ProrationAmountCents: amount, UnitAmountCents: snapshot.Unit, RemainingDays: remaining, CycleDays: days, PeriodStart: start.Format("2006-01-02"), PeriodEnd: end.Format("2006-01-02"), PaymentRequired: amount > 0, Simulation: s.Config.BranchPaymentSimulator}, nil
	})
}
func (s *Service) ConfirmBranch(ctx context.Context, actor, brand int64, quoteID, key string) (model.BranchOperation, error) {
	if !s.branchProrationAvailable() {
		return model.BranchOperation{}, ErrBillingUnavailable
	}
	if _, e := uuid.Parse(quoteID); e != nil {
		return model.BranchOperation{}, ErrInvalidRequest
	}
	if _, e := uuid.Parse(key); e != nil {
		return model.BranchOperation{}, ErrInvalidRequest
	}
	op, e := s.Repo.ConfirmBranchQuote(ctx, actor, brand, quoteID, key, s.Config.BranchPaymentSimulator, strings.TrimRight(s.Config.PublicAppURL, "/")+"/sucursal-pago", s.Now())
	if e != nil {
		return op, e
	}
	if e = s.reconcileBranch(ctx, op.ID); e != nil {
		return op, e
	}
	return s.Repo.BranchOperation(ctx, actor, brand, op.ID)
}
func (s *Service) BranchOperation(ctx context.Context, actor, brand int64, id string) (model.BranchOperation, error) {
	if !s.branchProrationAvailable() {
		return model.BranchOperation{}, ErrBillingUnavailable
	}
	if _, e := uuid.Parse(id); e != nil {
		return model.BranchOperation{}, ErrInvalidRequest
	}
	op, e := s.Repo.BranchOperation(ctx, actor, brand, id)
	if e != nil {
		return op, e
	}
	if e = s.reconcileBranch(ctx, id); e != nil {
		return op, e
	}
	return s.Repo.BranchOperation(ctx, actor, brand, id)
}
func (s *Service) SimulateBranch(ctx context.Context, actor, brand int64, id, action string) (model.BranchOperation, error) {
	if s.Config.Production || !s.Config.BranchPaymentSimulator {
		return model.BranchOperation{}, ErrForbidden
	}
	if _, e := uuid.Parse(id); e != nil {
		return model.BranchOperation{}, ErrInvalidRequest
	}
	if _, e := s.Repo.BranchOperation(ctx, actor, brand, id); e != nil {
		return model.BranchOperation{}, e
	}
	if e := s.Repo.SimulateBranch(ctx, brand, id, action, s.Now()); e != nil {
		return model.BranchOperation{}, e
	}
	return s.BranchOperation(ctx, actor, brand, id)
}
func (s *Service) reconcileBranch(ctx context.Context, id string) error {
	var provider repository.BranchBillingProvider
	if !s.Config.BranchPaymentSimulator {
		var ok bool
		provider, ok = s.Billing.(repository.BranchBillingProvider)
		if !ok {
			return ErrBillingUnavailable
		}
	}
	return s.Repo.ReconcileBranch(ctx, id, provider, s.Config.PublicAppURL, s.Now())
}
func (s *Service) RunBranchProration(ctx context.Context, logger *slog.Logger) {
	if !s.branchProrationAvailable() {
		return
	}
	tick := time.NewTicker(3 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			ids, e := s.Repo.PendingBranchOperations(ctx)
			if e != nil {
				logger.ErrorContext(ctx, "branch billing queue unavailable", "error", e)
				continue
			}
			for _, id := range ids {
				work, cancel := context.WithTimeout(ctx, 12*time.Second)
				e = s.reconcileBranch(work, id)
				cancel()
				if e != nil {
					logger.ErrorContext(ctx, "branch billing reconcile failed", "operation_id", id, "error", e)
				}
			}
		}
	}
}

// Provider notifications identify a resource; all economic fields are read back
// through authenticated provider API before they can affect a branch.
func (s *Service) applyBranchPaymentWebhook(ctx context.Context, paymentID string) (bool, error) {
	provider, ok := s.Billing.(interface {
		GetPayment(context.Context, string) (model.BillingPayment, error)
	})
	if !ok {
		return false, nil
	}
	payment, e := provider.GetPayment(ctx, paymentID)
	if e != nil {
		return false, ErrBillingProviderFailure
	}
	if !strings.HasPrefix(payment.ExternalReference, "puntazo:branch:") {
		return false, nil
	}
	id := strings.TrimPrefix(payment.ExternalReference, "puntazo:branch:")
	if _, e = uuid.Parse(id); e != nil {
		return true, ErrInvalidRequest
	}
	return true, s.reconcileBranch(ctx, id)
}
