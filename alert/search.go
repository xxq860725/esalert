package alert

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"time"

	"go.uber.org/zap"
	"github.com/CheerChen/esalert/logger"
)

type Hit struct {
	Index  string                 `json:"_index"`  // The index the hit came from
	Type   string                 `json:"_type"`   // The type the document is
	ID     string                 `json:"_id"`     // The unique id of the document
	Score  float64                `json:"_score"`  // The document's score relative to the search
	Source map[string]interface{} `json:"_source"` // The actual document
}

type HitInfo struct {
	HitCount    uint64  `json:"total"`     // The total number of documents matched
	HitMaxScore float64 `json:"max_score"` // The maximum score of all the documents matched
	Hits        []Hit   `json:"hits"`      // The actual documents matched
}

type Result struct {
	TookMS       uint64 `json:"took"`                         // Time search took to complete, in milliseconds
	TimedOut     bool   `json:"timed_out"`                    // Whether or not the search timed out
	HitInfo      `json:"hits" luautil:",inline"`              // Information related to the actual hits
	Aggregations map[string]interface{} `json:"aggregations"` // Information related to aggregations in the query
}

type Context struct {
	Name      string
	StartedTS uint64
	Result    `luautil:",inline"`
	time.Time `luautil:"-"`
}

type elasticError struct {
	Error string `json:"reason"`
}

// Dict represents a key-value map which may be unmarshalled from a yaml
// document. It is unique in that it enforces all the keys to be strings (where
// the default behavior in the yaml package is to have keys be interface{}), and
// for any embedded objects it find it will decode them into Dicts instead of
// map[interface{}]interface{}
type Dict map[string]interface{}

// UnmarshalYAML is used to unmarshal a yaml string into the Dict. See the
// dict's doc string for more details on what it is used for
func (d *Dict) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var m map[interface{}]interface{}
	if err := unmarshal(&m); err != nil {
		return err
	}

	var err error
	*d, err = mapToDict(m)
	return err
}

func mapToDict(m map[interface{}]interface{}) (Dict, error) {
	d := Dict{}
	for k, v := range m {
		ks, ok := k.(string)
		if !ok {
			return nil, fmt.Errorf("non-string key found: %v", ks)
		}
		switch vi := v.(type) {
		case map[interface{}]interface{}:
			vd, err := mapToDict(vi)
			if err != nil {
				return nil, err
			}
			d[ks] = vd

		case []interface{}:
			for i := range vi {
				if vid, ok := vi[i].(map[interface{}]interface{}); ok {
					vd, err := mapToDict(vid)
					if err != nil {
						return nil, err
					}
					vi[i] = vd
				}
			}
			d[ks] = vi

		default:
			d[ks] = vi
		}
	}
	return d, nil
}

// Search performs a search against the given elasticsearch index for
// documents of the given type. The search must json marshal into a valid
// elasticsearch request body query
// (see https://www.elastic.co/guide/en/elasticsearch/reference/current/search-request-body.html)
func Search(u string, query interface{}) (Result, error) {
	bodyReq, err := json.Marshal(query)
	if err != nil {
		return Result{}, err
	}
	method := "POST"
	logger.Info("search build request",
		zap.String("method", method),
		zap.String("url", u),
		zap.String("body", string(bodyReq)),
	)
	req, err := http.NewRequest(method, u, bytes.NewBuffer(bodyReq))
	if err != nil {
		return Result{}, err
	}
	req.Header.Add("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")

	//req.SetBasicAuth(config.Opts.ElasticSearchUser, config.Opts.ElasticSearchPass)
	client := &http.Client{
		Timeout: time.Duration(5 * time.Second),
	}
	resp, err := client.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return Result{}, err
	}

	logger.Info("search results",
		zap.String("body", string(body)),
	)

	if resp.StatusCode != 200 {
		var e elasticError
		if err := json.Unmarshal(body, &e); err != nil {
			logger.Error("could not unmarshal error body", zap.String("err", err.Error()))
			return Result{}, err
		}
		return Result{}, errors.New(fmt.Sprintf("HTTP status code: %v", resp.StatusCode))
	}

	var result Result
	if err := json.Unmarshal(body, &result); err != nil {
		logger.Error("could not unmarshal query result", zap.String("err", err.Error()))
		return result, err
	} else if result.TimedOut {
		return result, errors.New("query timed out in elastic query")
	}

	return result, nil
}
