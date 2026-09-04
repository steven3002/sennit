package recall

import (
	"strings"

	"github.com/steven3002/sennit/record"
)

// A Filter narrows a recall by preference.
//
// It boosts and it cannot do anything else. There is deliberately no predicate
// on this type, no Matches, no Passes, no Apply that returns a subset, because
// the difference between preferring and excluding is the difference between a
// slightly worse answer and no answer at all. Applied as an exclusion, a single
// wrong tag drops hit@5 from 0.949 to 0.729, below not filtering at all, and
// returns nothing whatsoever for ten queries in fifty-nine. The identical wrong
// filter applied as a boost scores 0.881, which is what no filter scores.
//
// A future caller who wants a hard filter cannot build one out of what this type
// offers. Boost is a magnitude, not a verdict, and nothing here reports whether
// a record matched.
type Filter struct {
	// Types prefers records of these kinds.
	Types []record.Type
	// Tags prefers records carrying these tags. Matching is graded: carrying
	// two of three requested tags is worth more than one, which is what makes a
	// filter with one wrong tag degrade rather than fall over.
	Tags []string
}

// Empty reports whether the filter would change any ranking.
func (f Filter) Empty() bool { return len(f.Types) == 0 && len(f.Tags) == 0 }

// Weights set how far a filter may move a record.
//
// They are additive on the rank-fusion scale the pipeline ranks in, so a boost
// is commensurable with a difference in rank and can be reasoned about in those
// terms: with the fusion constant at 60, one position near the top of the pool
// is worth about 0.00026, and the whole hundred-record pool spans about 0.010.
// A tag weight of 0.004 therefore says "carrying every requested tag is worth
// about fifteen places". A multiplicative form was rejected because it would
// scale the gap between ranks rather than move a record across it.
type Weights struct {
	Tag  float32
	Type float32
}

// DefaultWeights are the shipped weights, measured on the labelled corpus.
//
// They are stated against the fused score and not against cosine. Ranking used
// to be cosine alone, and these constants were an order of magnitude larger to
// suit it; fusion replaced that scale with one whose whole pool spans about
// 0.010, where the old tag weight of 0.20 was twenty times the entire spread.
// A boost that large does not prefer a record, it guarantees it, which is hard
// filtering under another name, and hard filtering is the thing measured to
// return nothing at all for a sixth of queries when a single tag is wrong.
// Carrying the old constants across the scale change would have converted soft
// filtering into the failure mode it exists to avoid.
//
// The criterion is unchanged, and it is not maximising the score under a correct
// filter: that number rises with the weight for the same bad reason. It is the
// largest boost that still costs a *wrong* filter nothing, since surviving a
// wrong filter is the entire point.
//
// Measured across three agent qualities on two labelled corpora, and the weight
// is the largest that costs a wrong filter nothing on *both*. On the 59-query
// corpus a correct filter takes hit@5 from 0.847 to 0.898 and MRR from 0.745 to
// 0.788, while a filter with one tag replaced scores 0.898 / 0.764 and a wholly
// wrong one scores the unfiltered baseline exactly. Doubling the weight is free
// on that corpus but not on the 20-query one, where a wholly wrong filter starts
// costing 5 points of hit@1; the two corpora disagree, so the lower of the two
// answers is the one that ships.
//
// Weight is not what limits filtering here. Doubling again reaches 0.932 with a
// correct filter, and quadrupling reaches 1.000 on the smaller corpus, both by
// letting a wrong filter do real damage, which is the trade soft filtering
// exists to refuse.
//
// The 3:1 ratio between the tag and type weights was held fixed through the
// sweep rather than optimised on its own; it reflects that tags discriminate
// more than types, which was measured separately, not here.
var DefaultWeights = Weights{Tag: 0.002, Type: 0.00067}

func (w Weights) orDefault() Weights {
	if w == (Weights{}) {
		return DefaultWeights
	}
	return w
}

// Meta is what ranking knows about a record before it has fetched it.
//
// Ranking has to decide an order before it reads any bodies, that is the whole
// point of holding an index, so anything the ordering depends on has to be
// available here rather than on the record itself.
type Meta struct {
	// Kind is the record's class. It is what a scope selects on, and it is
	// never an input to the boost: a class is a container, not a preference.
	Kind record.Kind
	Type record.Type
	Tags []string
}

// boost scores how well a record answers the filter, in [0, 1] per component.
//
// It is unexported and takes the metadata rather than hanging off Filter, so
// that the only way to reach it is through the ranking path, which always
// returns as many results as it was asked for.
func (f Filter) boost(w Weights, meta Meta) float32 {
	if f.Empty() {
		return 0
	}
	w = w.orDefault()
	var total float32

	if len(f.Tags) > 0 {
		carried := make(map[string]bool, len(meta.Tags))
		for _, tag := range meta.Tags {
			carried[normalizeTag(tag)] = true
		}
		var matched int
		for _, want := range f.Tags {
			if carried[normalizeTag(want)] {
				matched++
			}
		}
		total += w.Tag * float32(matched) / float32(len(f.Tags))
	}

	for _, want := range f.Types {
		if want == meta.Type {
			total += w.Type
			break
		}
	}
	return total
}

// normalizeTag matches the form the device stores tags in, so a filter and a
// record that mean the same tag agree regardless of how either was typed.
func normalizeTag(tag string) string { return strings.ToLower(strings.TrimSpace(tag)) }
