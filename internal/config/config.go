// Package config reads the bot's settings from the environment, with a .env file in
// the working directory read first (KEY=value lines; # comments; neither overrides a
// variable already set).
package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is everything the bot is told.
type Config struct {
	Token       string        // GATHER_DISCORD_TOKEN
	ChannelID   string        // GATHER_CHANNEL_ID: the one channel the bot listens and talks in
	ServerAddr  string        // GATHER_SERVER_ADDR: host:port the players are told to join
	Listen      string        // GATHER_LISTEN: the API's address, ":8080" by default
	Secret      string        // GATHER_SECRET: shared with the server's script
	Prefix      string        // GATHER_PREFIX: "!beta_" by default
	Maps        []string      // GATHER_MAPS: the pool, space-separated; the CTF maps by default
	TeamSize    int           // GATHER_TEAM_SIZE: 3 by default
	PickTimeout time.Duration // GATHER_PICK_TIMEOUT: seconds, 90 by default
	Grace       int           // GATHER_GRACE: seconds to say /pw, 30 by default; must match the script's
}

// DefaultMaps is bettersoldat's CTF maps (assets/maps/ctf_*.pms).
var DefaultMaps = []string{
	"ctf_Aftermath", "ctf_Amnesia", "ctf_Ash", "ctf_B2b", "ctf_Blade", "ctf_Campeche", "ctf_Cobra",
	"ctf_Crucifix", "ctf_Death", "ctf_Division", "ctf_Dropdown", "ctf_Equinox", "ctf_Guardian",
	"ctf_Hormone", "ctf_IceBeam", "ctf_Kampf", "ctf_Lanubya", "ctf_Laos", "ctf_MFM", "ctf_Maya",
	"ctf_Mayapan", "ctf_Nuubia", "ctf_Raspberry", "ctf_Rotten", "ctf_Ruins", "ctf_Run", "ctf_Scorpion",
	"ctf_Snakebite", "ctf_Steel", "ctf_Triumph", "ctf_Viet", "ctf_Voland", "ctf_Wretch", "ctf_X",
}

// Load reads .env (if there is one) and the environment.
func Load() (Config, error) {
	if err := loadDotEnv(".env"); err != nil {
		return Config{}, err
	}
	c := Config{
		Token:      os.Getenv("GATHER_DISCORD_TOKEN"),
		ChannelID:  os.Getenv("GATHER_CHANNEL_ID"),
		ServerAddr: os.Getenv("GATHER_SERVER_ADDR"),
		Listen:     getenv("GATHER_LISTEN", ":8080"),
		Secret:     os.Getenv("GATHER_SECRET"),
		Prefix:     getenv("GATHER_PREFIX", "!beta_"),
		Maps:       strings.Fields(os.Getenv("GATHER_MAPS")),
	}
	var errs []error
	for name, v := range map[string]string{"GATHER_DISCORD_TOKEN": c.Token, "GATHER_CHANNEL_ID": c.ChannelID, "GATHER_SERVER_ADDR": c.ServerAddr, "GATHER_SECRET": c.Secret} {
		if v == "" {
			errs = append(errs, fmt.Errorf("%s is not set", name))
		}
	}
	if len(c.Maps) == 0 {
		c.Maps = DefaultMaps
	}
	var err error
	if c.TeamSize, err = intenv("GATHER_TEAM_SIZE", 3); err != nil {
		errs = append(errs, err)
	}
	seconds, err := intenv("GATHER_PICK_TIMEOUT", 90)
	if err != nil {
		errs = append(errs, err)
	}
	c.PickTimeout = time.Duration(seconds) * time.Second
	if c.Grace, err = intenv("GATHER_GRACE", 30); err != nil {
		errs = append(errs, err)
	}
	return c, errors.Join(errs...)
}

func getenv(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func intenv(name string, fallback int) (int, error) {
	v := os.Getenv(name)
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s must be a positive number, not %q", name, v)
	}
	return n, nil
}

// loadDotEnv sets the variables of path that aren't set already. No file is fine.
func loadDotEnv(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return fmt.Errorf("%s:%d: not KEY=value", path, n)
		}
		key = strings.TrimSpace(strings.TrimPrefix(key, "export "))
		value = strings.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '"' && value[len(value)-1] == '"' || value[0] == '\'' && value[len(value)-1] == '\'') {
			value = value[1 : len(value)-1]
		}
		if _, set := os.LookupEnv(key); !set {
			os.Setenv(key, value)
		}
	}
	return sc.Err()
}
