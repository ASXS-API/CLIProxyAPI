package config

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// RetryIntervalSeconds stores retry interval configuration as seconds while
// accepting either legacy numeric seconds or duration strings like "300ms".
type RetryIntervalSeconds float64

func (v RetryIntervalSeconds) Duration() time.Duration {
	if v <= 0 {
		return 0
	}
	return time.Duration(float64(v) * float64(time.Second))
}

func (v RetryIntervalSeconds) String() string {
	seconds := float64(v)
	if math.Trunc(seconds) == seconds {
		return strconv.FormatInt(int64(seconds), 10)
	}
	return v.Duration().String()
}

func (v RetryIntervalSeconds) MarshalJSON() ([]byte, error) {
	return []byte(strconv.FormatFloat(float64(v), 'f', -1, 64)), nil
}

func (v *RetryIntervalSeconds) UnmarshalJSON(data []byte) error {
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	parsed, err := parseRetryIntervalValue(raw)
	if err != nil {
		return err
	}
	*v = parsed
	return nil
}

func (v RetryIntervalSeconds) MarshalYAML() (any, error) {
	seconds := float64(v)
	if math.Trunc(seconds) == seconds {
		return int64(seconds), nil
	}
	return v.Duration().String(), nil
}

func (v *RetryIntervalSeconds) UnmarshalYAML(value *yaml.Node) error {
	if value == nil {
		return nil
	}
	parsed, err := parseRetryIntervalString(value.Value)
	if err != nil {
		return err
	}
	*v = parsed
	return nil
}

func parseRetryIntervalValue(raw any) (RetryIntervalSeconds, error) {
	switch v := raw.(type) {
	case float64:
		return retryIntervalFromSeconds(v)
	case string:
		return parseRetryIntervalString(v)
	default:
		return 0, fmt.Errorf("max-retry-interval must be a number of seconds or duration string")
	}
}

func parseRetryIntervalString(raw string) (RetryIntervalSeconds, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0, nil
	}
	if seconds, err := strconv.ParseFloat(value, 64); err == nil {
		return retryIntervalFromSeconds(seconds)
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("invalid max-retry-interval %q: use seconds or a duration like 300ms", raw)
	}
	return retryIntervalFromDuration(duration)
}

func retryIntervalFromSeconds(seconds float64) (RetryIntervalSeconds, error) {
	if seconds < 0 {
		return 0, nil
	}
	return RetryIntervalSeconds(seconds), nil
}

func retryIntervalFromDuration(duration time.Duration) (RetryIntervalSeconds, error) {
	if duration < 0 {
		return 0, nil
	}
	return RetryIntervalSeconds(float64(duration) / float64(time.Second)), nil
}
