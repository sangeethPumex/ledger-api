package brainjogger

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"ledger-backend/internal/ist"
)

// staleAfter is how long past the due time a missed alert (server was down) is still worth sending.
const staleAfter = 10 * time.Minute

type Service struct {
	col, subs *mongo.Collection
	pub, priv string // VAPID keys; push is off when either is empty
	subject   string
}

func NewService(db *mongo.Database) *Service {
	s := &Service{col: db.Collection(Collection), subs: db.Collection(PushCollection),
		pub: os.Getenv("VAPID_PUBLIC_KEY"), priv: os.Getenv("VAPID_PRIVATE_KEY"), subject: os.Getenv("VAPID_SUBJECT")}
	if s.subject == "" {
		s.subject = "mailto:admin@example.com"
	}
	if !s.PushEnabled() {
		log.Printf("brainjogger: VAPID_PUBLIC_KEY / VAPID_PRIVATE_KEY not set, push notifications are off")
	}
	return s
}

func (s *Service) PushEnabled() bool { return s.pub != "" && s.priv != "" }
func (s *Service) PublicKey() string { return s.pub }

// List returns every list, soonest first.
func (s *Service) List(ctx context.Context) ([]List, error) {
	cur, err := s.col.Find(ctx, bson.D{}, options.Find().SetSort(bson.D{{Key: "remind_at", Value: 1}}))
	if err != nil {
		return nil, err
	}
	out := []List{}
	return out, cur.All(ctx, &out)
}

func (s *Service) Create(ctx context.Context, l List) error {
	l.ID, l.CreatedAt = bson.NewObjectID(), time.Now().UTC()
	_, err := s.col.InsertOne(ctx, l)
	return err
}

// Update replaces the editable fields. Alerts are re-armed, so moving the time makes them fire again.
func (s *Service) Update(ctx context.Context, id bson.ObjectID, l List) error {
	r, err := s.col.UpdateByID(ctx, id, bson.M{"$set": bson.M{"title": l.Title, "kind": l.Kind, "note": l.Note, "remind_at": l.RemindAt,
		"lead_mins": l.LeadMins, "notified": []int{}, "items": l.Items}})
	if err == nil && r.MatchedCount == 0 {
		return mongo.ErrNoDocuments
	}
	return err
}

func (s *Service) Delete(ctx context.Context, id bson.ObjectID) error {
	r, err := s.col.DeleteOne(ctx, bson.M{"_id": id})
	if err == nil && r.DeletedCount == 0 {
		return mongo.ErrNoDocuments
	}
	return err
}

// SetBought ticks or unticks one item. Once nothing is left to buy the whole list is deleted and deleted is true.
func (s *Service) SetBought(ctx context.Context, id bson.ObjectID, itemID string, bought bool) (deleted bool, err error) {
	var l List
	err = s.col.FindOneAndUpdate(ctx, bson.M{"_id": id, "items.id": itemID}, bson.M{"$set": bson.M{"items.$[i].bought": bought}},
		options.FindOneAndUpdate().SetReturnDocument(options.After).SetArrayFilters([]any{bson.M{"i.id": itemID}})).Decode(&l)
	if err != nil {
		return false, err
	}
	if n, _ := l.Left(); n == 0 {
		return true, s.Delete(ctx, id)
	}
	return false, nil
}

// Subscription is a browser push endpoint as the Push API reports it.
type Subscription struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

func (s *Service) Subscribe(ctx context.Context, sub Subscription) error {
	_, err := s.subs.ReplaceOne(ctx, bson.M{"_id": sub.Endpoint}, bson.M{"_id": sub.Endpoint, "p256dh": sub.Keys.P256dh, "auth": sub.Keys.Auth, "created_at": time.Now().UTC()},
		options.Replace().SetUpsert(true))
	return err
}

func (s *Service) Unsubscribe(ctx context.Context, endpoint string) error {
	_, err := s.subs.DeleteOne(ctx, bson.M{"_id": endpoint})
	return err
}

type payload struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	Tag   string `json:"tag"`
	URL   string `json:"url"`
}

// Notify pushes to every subscribed device and forgets the ones the push service says are gone. It returns how many were reached.
func (s *Service) Notify(ctx context.Context, p payload) int {
	if !s.PushEnabled() {
		return 0
	}
	cur, err := s.subs.Find(ctx, bson.D{})
	if err != nil {
		log.Printf("brainjogger: list subscriptions: %v", err)
		return 0
	}
	var subs []struct {
		Endpoint string `bson:"_id"`
		P256dh   string `bson:"p256dh"`
		Auth     string `bson:"auth"`
	}
	if err := cur.All(ctx, &subs); err != nil {
		return 0
	}
	body, _ := json.Marshal(p)
	sent := 0
	for _, sub := range subs {
		res, err := webpush.SendNotificationWithContext(ctx, body, &webpush.Subscription{Endpoint: sub.Endpoint, Keys: webpush.Keys{P256dh: sub.P256dh, Auth: sub.Auth}},
			&webpush.Options{Subscriber: s.subject, VAPIDPublicKey: s.pub, VAPIDPrivateKey: s.priv, TTL: 3600, Urgency: webpush.UrgencyHigh})
		if err != nil {
			log.Printf("brainjogger: push: %v", err)
			continue
		}
		res.Body.Close()
		switch {
		case res.StatusCode == 404 || res.StatusCode == 410:
			_ = s.Unsubscribe(ctx, sub.Endpoint)
		case res.StatusCode < 300:
			sent++
		default:
			log.Printf("brainjogger: push service replied %d", res.StatusCode)
		}
	}
	return sent
}

func (s *Service) Test(ctx context.Context) int {
	return s.Notify(ctx, payload{Title: "Brain Jogger", Body: "Notifications are working.", Tag: "jogger-test", URL: "/jogger"})
}

func message(l List, lead int) payload {
	when := l.RemindAt.In(ist.Loc).Format("3:04 pm")
	in := fmt.Sprintf("in %d min", lead)
	if lead == 0 {
		in = "now"
	}
	body := fmt.Sprintf("%s · %s", l.Title, when)
	if n, paise := l.Left(); l.Kind == "shopping" && n > 0 {
		body += fmt.Sprintf(" · %d left", n)
		if paise > 0 {
			body += fmt.Sprintf(", about ₹%d", (paise+50)/100)
		}
	} else if l.Note != "" {
		body += " · " + l.Note
	}
	head := "Reminder " + in
	if l.Kind == "shopping" {
		head = "Shopping " + in
	}
	return payload{Title: head, Body: body, Tag: "jogger-" + l.ID.Hex(), URL: "/jogger"}
}

// Tick sends every alert that has come due. An alert is recorded as sent before it goes out, so a crash never repeats one.
func (s *Service) Tick(ctx context.Context, now time.Time) {
	cur, err := s.col.Find(ctx, bson.M{"remind_at": bson.M{"$gte": now.Add(-staleAfter), "$lte": now.Add(maxLeadMins * time.Minute)}})
	if err != nil {
		log.Printf("brainjogger: tick: %v", err)
		return
	}
	var ls []List
	if err := cur.All(ctx, &ls); err != nil {
		return
	}
	for _, l := range ls {
		due := l.Due(now)
		if len(due) == 0 {
			continue
		}
		if _, err := s.col.UpdateByID(ctx, l.ID, bson.M{"$addToSet": bson.M{"notified": bson.M{"$each": due}}}); err != nil {
			continue
		}
		// Several can fall due together (a list made at 3:40 for 4:00); one message for the closest is enough.
		s.Notify(ctx, message(l, due[0]))
	}
}

// Run checks for due alerts until ctx ends.
func (s *Service) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		s.Tick(ctx, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
