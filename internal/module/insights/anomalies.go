package insights

import (
	"sort"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

const (
	anomalyDays   = 14      // only recent payments are worth an alert
	spikeFactor   = 4       // "4x your usual"
	spikeMinExtra = 50_000  // and at least ₹500 above it, so ₹50 vs ₹200 stays quiet
	newVendorMin  = 200_000 // an unknown payee below ₹2,000 is not worth an alert
)

type Anomaly struct {
	TxnID      bson.ObjectID `json:"txn_id"`
	Vendor     string        `json:"vendor"`
	At         time.Time     `json:"at"`
	Paise      int64         `json:"paise"`
	UsualPaise int64         `json:"usual_paise"` // the vendor's usual payment (spike) or the usual payment overall (new_vendor)
	Ratio      float64       `json:"ratio"`
	Kind       string        `json:"kind"` // spike: far above what this vendor usually costs; new_vendor: a big payment to an unknown payee
}

// detectAnomalies flags recent payments that stand out. Pure.
func detectAnomalies(all []vendorSeries, now time.Time) []Anomaly {
	var every []int64
	for _, s := range all {
		for _, p := range s.Txns {
			every = append(every, p.Paise)
		}
	}
	overall := median(every)
	cutoff := now.AddDate(0, 0, -anomalyDays)
	out := []Anomaly{}
	for _, s := range all {
		for i, p := range s.Txns {
			if p.At.Before(cutoff) {
				continue
			}
			prior := make([]int64, i)
			for j := range prior {
				prior[j] = s.Txns[j].Paise
			}
			switch usual := median(prior); {
			case len(prior) >= 3 && p.Paise >= spikeFactor*usual && p.Paise-usual >= spikeMinExtra:
				out = append(out, Anomaly{p.ID, s.Name, p.At, p.Paise, usual, float64(p.Paise) / float64(usual), "spike"})
			case len(prior) == 0 && (s.VendorID == nil || p.Review) && p.Paise >= max(newVendorMin, spikeFactor*overall):
				out = append(out, Anomaly{p.ID, s.Name, p.At, p.Paise, overall, float64(p.Paise) / float64(max(overall, 1)), "new_vendor"})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	if len(out) > 10 {
		out = out[:10]
	}
	return out
}
