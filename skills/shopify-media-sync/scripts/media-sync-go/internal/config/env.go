package config

import (
	"bufio"
	"os"
	"regexp"
	"strings"
)

var supportedDotEnvKeyRE = regexp.MustCompile(`^SHOPIFY_(?:CLIENT_ID|CLIENT_SECRET|ADMIN_TOKEN|API_VERSION)(?:_[A-Z0-9_]+)?$`)

func LoadDotEnv(path string) error {
	values, err := ReadDotEnv(path)
	if err != nil {
		return err
	}
	for key, value := range values {
		if !supportedDotEnvKeyRE.MatchString(key) {
			continue
		}
		if _, exists := os.LookupEnv(key); !exists {
			_ = os.Setenv(key, value)
		}
	}
	return nil
}

func ReadDotEnv(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	values := map[string]string{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		value = strings.Trim(value, `"'`)
		if key == "" {
			continue
		}
		values[key] = value
	}
	return values, scanner.Err()
}
