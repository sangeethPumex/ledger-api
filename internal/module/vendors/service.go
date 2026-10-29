package vendors

import (
	"context"
	"errors"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

var ErrInvalid = errors.New("invalid vendor")

type Service struct {
	col *mongo.Collection
	txs *mongo.Collection // owned by the transactions module; unlinked when a vendor is deleted
	// OnChange runs after any write that can change how transactions resolve.
	OnChange func(ctx context.Context)
}

func NewService(ctx context.Context, db *mongo.Database) (*Service, error) {
	col := db.Collection(Collection)
	// Sparse + omitempty aliases: a given alias can belong to only one vendor.
	_, err := col.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "name", Value: 1}}, Options: options.Index().SetUnique(true)},
		{Keys: bson.D{{Key: "aliases", Value: 1}}, Options: options.Index().SetUnique(true).SetSparse(true)},
	})
	return &Service{col: col, txs: db.Collection("transactions")}, err
}

func (s *Service) changed(ctx context.Context) {
	if s.OnChange != nil {
		s.OnChange(ctx)
	}
}

func cleanAliases(in []string) []string {
	out := []string{}
	for _, a := range in {
		if a = Normalize(a); a != "" {
			out = append(out, a)
		}
	}
	return out
}

func (s *Service) Create(ctx context.Context, name string, cat *bson.ObjectID, aliases []string) (*Vendor, error) {
	v := &Vendor{ID: bson.NewObjectID(), Name: strings.TrimSpace(name), CategoryID: cat, Aliases: cleanAliases(aliases)}
	if v.Name == "" {
		return nil, ErrInvalid
	}
	if _, err := s.col.InsertOne(ctx, v); err != nil {
		return nil, err
	}
	s.changed(ctx)
	return v, nil
}

// Update changes whichever of name, category and aliases are non-nil. Aliases replace the whole list.
func (s *Service) Update(ctx context.Context, id bson.ObjectID, name *string, cat *bson.ObjectID, aliases *[]string) error {
	set := bson.M{}
	if name != nil {
		n := strings.TrimSpace(*name)
		if n == "" {
			return ErrInvalid
		}
		set["name"] = n
	}
	if cat != nil {
		set["category_id"] = *cat
	}
	if aliases != nil {
		set["aliases"] = cleanAliases(*aliases)
	}
	if len(set) == 0 {
		return ErrInvalid
	}
	res, err := s.col.UpdateByID(ctx, id, bson.M{"$set": set})
	if err == nil && res.MatchedCount == 0 {
		err = mongo.ErrNoDocuments
	}
	if err == nil {
		s.changed(ctx)
	}
	return err
}

// Delete removes a vendor. Its transactions go back to review, unless another vendor still matches them.
func (s *Service) Delete(ctx context.Context, id bson.ObjectID) error {
	res, err := s.col.DeleteOne(ctx, bson.M{"_id": id})
	if err != nil {
		return err
	}
	if res.DeletedCount == 0 {
		return mongo.ErrNoDocuments
	}
	if _, err := s.txs.UpdateMany(ctx, bson.M{"vendor_id": id}, bson.M{
		"$set":   bson.M{"needs_review": true, "review_reason": "unknown_vendor"},
		"$unset": bson.M{"vendor_id": "", "category_id": ""},
	}); err != nil {
		return err
	}
	s.changed(ctx)
	return nil
}

// Merge folds vendor `from` into `into` ("SWIGGY" into "Swiggy Instamart"): its transactions move over, and its name and aliases become aliases
// of the target so later messages still resolve. All of it commits together or not at all (a multi-document transaction), so a failure
// halfway never leaves transactions pointing at a deleted vendor. The target's category then reaches the moved transactions via OnChange.
func (s *Service) Merge(ctx context.Context, from, into bson.ObjectID) error {
	if from == into {
		return ErrInvalid
	}
	sess, err := s.col.Database().Client().StartSession()
	if err != nil {
		return err
	}
	defer sess.EndSession(ctx)
	_, err = sess.WithTransaction(ctx, func(ctx context.Context) (any, error) {
		var src Vendor
		if err := s.col.FindOne(ctx, bson.M{"_id": from}).Decode(&src); err != nil {
			return nil, err
		}
		// Delete first: aliases are unique across vendors, so the target cannot take them while the source still holds them.
		if _, err := s.col.DeleteOne(ctx, bson.M{"_id": from}); err != nil {
			return nil, err
		}
		res, err := s.col.UpdateByID(ctx, into, bson.M{"$addToSet": bson.M{"aliases": bson.M{"$each": append(cleanAliases(src.Aliases), Normalize(src.Name))}}})
		if err != nil {
			return nil, err
		}
		if res.MatchedCount == 0 {
			return nil, mongo.ErrNoDocuments
		}
		_, err = s.txs.UpdateMany(ctx, bson.M{"vendor_id": from}, bson.M{"$set": bson.M{"vendor_id": into}})
		return nil, err
	})
	if err == nil {
		s.changed(ctx)
	}
	return err
}

func (s *Service) AddAlias(ctx context.Context, id bson.ObjectID, alias string) error {
	a := Normalize(alias)
	if a == "" {
		return ErrInvalid
	}
	res, err := s.col.UpdateByID(ctx, id, bson.M{"$addToSet": bson.M{"aliases": a}})
	if err == nil && res.MatchedCount == 0 {
		err = mongo.ErrNoDocuments
	}
	if err == nil {
		s.changed(ctx)
	}
	return err
}

func (s *Service) List(ctx context.Context) ([]Vendor, error) {
	cur, err := s.col.Find(ctx, bson.D{}, options.Find().SetSort(bson.D{{Key: "name", Value: 1}}))
	if err != nil {
		return nil, err
	}
	out := []Vendor{}
	return out, cur.All(ctx, &out)
}

// Match returns the first vendor whose name equals name or whose alias occurs
// in any of texts. nil means unknown. A short alias matches broadly, so use
// specific ones (a full UPI id, not "ola").
func (s *Service) Match(ctx context.Context, name string, texts ...string) (*Vendor, error) {
	all, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	name = Normalize(name)
	for i := range all {
		v := &all[i]
		if name != "" && Normalize(v.Name) == name {
			return v, nil
		}
		for _, a := range v.Aliases {
			for _, t := range texts {
				if strings.Contains(strings.ToLower(t), a) {
					return v, nil
				}
			}
		}
	}
	return nil, nil
}
