package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type WeatherTool struct{}

func NewWeatherTool() *WeatherTool {
	return &WeatherTool{}
}

func (t *WeatherTool) Name() string { return "weather" }

func (t *WeatherTool) Description() string {
	return "Get current weather and 3-day forecast for a city. Use when the user asks about weather or climate conditions."
}

func (t *WeatherTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"location": map[string]interface{}{
				"type":        "string",
				"description": "City name (e.g., 'Buenos Aires', 'London')",
			},
		},
		"required": []string{"location"},
	}
}

func (t *WeatherTool) Execute(ctx context.Context, args map[string]interface{}) *ToolResult {
	location, _ := args["location"].(string)
	if location == "" {
		return ErrorResult("location is required")
	}

	// Geocode the location
	lat, lon, name, err := geocodeLocation(ctx, location)
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to find location: %v", err))
	}

	// Fetch forecast
	weather, err := fetchWeather(ctx, lat, lon)
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to fetch weather: %v", err))
	}

	result := formatWeather(name, weather)
	return SilentResult(result)
}

func geocodeLocation(ctx context.Context, city string) (float64, float64, string, error) {
	geoURL := fmt.Sprintf("https://geocoding-api.open-meteo.com/v1/search?name=%s&count=1&language=es",
		url.QueryEscape(city))

	req, err := http.NewRequestWithContext(ctx, "GET", geoURL, nil)
	if err != nil {
		return 0, 0, "", err
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, 0, "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, 0, "", err
	}

	var geoResp struct {
		Results []struct {
			Name      string  `json:"name"`
			Latitude  float64 `json:"latitude"`
			Longitude float64 `json:"longitude"`
			Country   string  `json:"country"`
		} `json:"results"`
	}

	if err := json.Unmarshal(body, &geoResp); err != nil {
		return 0, 0, "", fmt.Errorf("failed to parse geocoding response: %w", err)
	}

	if len(geoResp.Results) == 0 {
		return 0, 0, "", fmt.Errorf("location '%s' not found", city)
	}

	r := geoResp.Results[0]
	displayName := r.Name
	if r.Country != "" {
		displayName += ", " + r.Country
	}
	return r.Latitude, r.Longitude, displayName, nil
}

type weatherData struct {
	Current struct {
		Temperature float64 `json:"temperature_2m"`
		Humidity    float64 `json:"relative_humidity_2m"`
		WindSpeed   float64 `json:"wind_speed_10m"`
		WeatherCode int     `json:"weather_code"`
	} `json:"current"`
	Daily struct {
		Time              []string  `json:"time"`
		TempMax           []float64 `json:"temperature_2m_max"`
		TempMin           []float64 `json:"temperature_2m_min"`
		PrecipProbability []float64 `json:"precipitation_probability_max"`
		WeatherCode       []int     `json:"weather_code"`
	} `json:"daily"`
}

func fetchWeather(ctx context.Context, lat, lon float64) (*weatherData, error) {
	weatherURL := fmt.Sprintf(
		"https://api.open-meteo.com/v1/forecast?latitude=%.4f&longitude=%.4f"+
			"&current=temperature_2m,relative_humidity_2m,wind_speed_10m,weather_code"+
			"&daily=temperature_2m_max,temperature_2m_min,precipitation_probability_max,weather_code"+
			"&timezone=auto&forecast_days=3",
		lat, lon)

	req, err := http.NewRequestWithContext(ctx, "GET", weatherURL, nil)
	if err != nil {
		return nil, err
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var data weatherData
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("failed to parse weather response: %w", err)
	}

	return &data, nil
}

func formatWeather(location string, w *weatherData) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("Clima en %s:\n\n", location))
	sb.WriteString(fmt.Sprintf("Ahora: %s, %.1f°C, humedad %0.f%%, viento %.1f km/h\n\n",
		weatherCodeToSpanish(w.Current.WeatherCode),
		w.Current.Temperature,
		w.Current.Humidity,
		w.Current.WindSpeed))

	sb.WriteString("Pronóstico:\n")
	for i, date := range w.Daily.Time {
		if i >= len(w.Daily.TempMax) {
			break
		}
		sb.WriteString(fmt.Sprintf("- %s: %s, %.0f°C / %.0f°C, lluvia %0.f%%\n",
			date,
			weatherCodeToSpanish(w.Daily.WeatherCode[i]),
			w.Daily.TempMin[i],
			w.Daily.TempMax[i],
			w.Daily.PrecipProbability[i]))
	}

	return sb.String()
}

func weatherCodeToSpanish(code int) string {
	switch code {
	case 0:
		return "Despejado"
	case 1:
		return "Mayormente despejado"
	case 2:
		return "Parcialmente nublado"
	case 3:
		return "Nublado"
	case 45, 48:
		return "Niebla"
	case 51, 53, 55:
		return "Llovizna"
	case 56, 57:
		return "Llovizna helada"
	case 61, 63, 65:
		return "Lluvia"
	case 66, 67:
		return "Lluvia helada"
	case 71, 73, 75:
		return "Nieve"
	case 77:
		return "Granizo"
	case 80, 81, 82:
		return "Chubascos"
	case 85, 86:
		return "Chubascos de nieve"
	case 95:
		return "Tormenta"
	case 96, 99:
		return "Tormenta con granizo"
	default:
		return fmt.Sprintf("Código %d", code)
	}
}
