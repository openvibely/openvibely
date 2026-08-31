package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/openvibely/openvibely/internal/chatcontrol"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

// PersonalityInfo holds display information for a personality option.
type PersonalityInfo struct {
	Key         string
	Name        string
	Description string
	IsCustom    bool
}

type CustomPersonalitySaveInput struct {
	Mode         string `json:"mode"`
	Name         string `json:"name"`
	Key          string `json:"key"`
	Description  string `json:"description"`
	SystemPrompt string `json:"system_prompt"`
	Activate     bool   `json:"activate"`
}

type CustomPersonalitySaveOptions struct {
	Input                 json.RawMessage
	CustomPersonalityRepo *repository.CustomPersonalityRepo
	SettingsRepo          *repository.SettingsRepo
}

type customPersonalitySaveResponse struct {
	OK          bool   `json:"ok"`
	Mode        string `json:"mode"`
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Activated   bool   `json:"activated"`
	Message     string `json:"message"`
}

// NormalizeCustomPersonalityKey creates a URL-safe key from a user-provided name or key.
func NormalizeCustomPersonalityKey(value string) string {
	key := strings.ToLower(strings.TrimSpace(value))
	key = strings.ReplaceAll(key, " ", "_")
	key = strings.ReplaceAll(key, "-", "_")
	var sb strings.Builder
	for _, r := range key {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

func ValidateCustomPersonalityFields(name, systemPrompt string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("Name is required")
	}
	prompt := strings.TrimSpace(systemPrompt)
	if prompt == "" {
		return fmt.Errorf("System prompt is required")
	}
	if len(prompt) < 20 {
		return fmt.Errorf("System prompt must be at least 20 characters")
	}
	return nil
}

func NewCustomPersonalityFromFields(name, key, description, systemPrompt string) (*models.CustomPersonality, error) {
	name = strings.TrimSpace(name)
	description = strings.TrimSpace(description)
	systemPrompt = strings.TrimSpace(systemPrompt)
	if err := ValidateCustomPersonalityFields(name, systemPrompt); err != nil {
		return nil, err
	}
	return &models.CustomPersonality{
		Name:         name,
		Key:          strings.TrimSpace(key),
		Description:  description,
		SystemPrompt: systemPrompt,
	}, nil
}

// presetPersonalities returns the hardcoded personality presets.
func presetPersonalities() []PersonalityInfo {
	return []PersonalityInfo{
		{Key: "", Name: "Base", Description: "Standard professional assistant tone"},
		{Key: "sarcastic_engineer", Name: "Sarcastic Engineer", Description: "Dry wit, eye-rolling at bad code, snarky but helpful"},
		{Key: "no_nonsense_pro", Name: "No-Nonsense Pro", Description: "Blunt, direct, zero fluff — straight to solutions"},
		{Key: "optimistic_mentor", Name: "Optimistic Mentor", Description: "Encouraging, celebrates small wins, supportive teaching style"},
		{Key: "academic_professor", Name: "Academic Professor", Description: "Formal, references theory, explains the \"why\" behind everything"},
		{Key: "zen_debugger", Name: "Zen Debugger", Description: "Calm, meditative approach — mindful and measured"},
		{Key: "caffeinated_hacker", Name: "Caffeinated Hacker", Description: "Enthusiastic, high energy, lots of exclamation points!!"},
		{Key: "startup_hustler", Name: "Startup Hustler", Description: "Action-oriented, MVP mindset, move fast and crush bugs"},
		{Key: "game_master", Name: "Game Master", Description: "Treats debugging like an RPG quest with narration"},
		{Key: "dad_joke_developer", Name: "Dad Joke Developer", Description: "Terrible programming puns and groan-worthy humor"},
		{Key: "pirate_captain", Name: "Pirate Captain", Description: "Pirate speak throughout — ahoy, shiver me timbers!"},
		{Key: "movie_quote_bot", Name: "Movie Quote Bot", Description: "References movies and pop culture constantly"},
		{Key: "time_traveler", Name: "Time Traveler", Description: "Speaks from different time periods about technology"},
		{Key: "security_paranoid", Name: "Security Paranoid", Description: "Always worried about vulnerabilities, security-first"},
		{Key: "performance_obsessed", Name: "Performance Obsessed", Description: "Everything is about speed and optimization"},
		{Key: "accessibility_champion", Name: "Accessibility Champion", Description: "Always thinking about a11y and inclusive design"},
	}
}

// AllPersonalities returns the list of available personality presets in display order.
func AllPersonalities() []PersonalityInfo {
	return presetPersonalities()
}

// AllPersonalitiesWithCustom returns presets merged with custom personalities from the database.
// Custom personalities appear after the presets.
func AllPersonalitiesWithCustom(ctx context.Context, repo *repository.CustomPersonalityRepo) []PersonalityInfo {
	result := presetPersonalities()
	if repo == nil {
		return result
	}
	customs, err := repo.List(ctx)
	if err != nil {
		return result
	}
	for _, c := range customs {
		result = append(result, customToPersonalityInfo(c))
	}
	return result
}

// customToPersonalityInfo converts a CustomPersonality model to PersonalityInfo.
func customToPersonalityInfo(c models.CustomPersonality) PersonalityInfo {
	return PersonalityInfo{
		Key:         c.Key,
		Name:        c.Name,
		Description: c.Description,
		IsCustom:    true,
	}
}

// IsPresetPersonality returns true if the given key belongs to a hardcoded preset.
func IsPresetPersonality(key string) bool {
	_, ok := personalityPrompts[key]
	return ok || key == ""
}

// FindPersonality returns the matching built-in or custom personality for an exact key.
func FindPersonality(ctx context.Context, key string, repo *repository.CustomPersonalityRepo) (PersonalityInfo, bool) {
	for _, personality := range AllPersonalitiesWithCustom(ctx, repo) {
		if personality.Key == key {
			return personality, true
		}
	}
	return PersonalityInfo{}, false
}

// IsAvailablePersonalityKey reports whether key is empty/default, a built-in preset, or an existing custom personality.
func IsAvailablePersonalityKey(ctx context.Context, key string, repo *repository.CustomPersonalityRepo) bool {
	_, ok := FindPersonality(ctx, key, repo)
	return ok
}

func ExecuteSaveCustomPersonalityRuntime(ctx context.Context, opts CustomPersonalitySaveOptions) (string, error) {
	if opts.CustomPersonalityRepo == nil {
		return "", fmt.Errorf("save_custom_personality: custom personality repository not configured")
	}
	var req CustomPersonalitySaveInput
	if err := chatcontrol.DecodeRuntimeToolInput(opts.Input, &req); err != nil {
		return "", err
	}
	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	if mode == "" {
		return "", fmt.Errorf("save_custom_personality requires mode create or update")
	}
	if mode != "create" && mode != "update" {
		return "", fmt.Errorf("save_custom_personality mode must be create or update")
	}
	if req.Activate && opts.SettingsRepo == nil {
		return "", fmt.Errorf("save_custom_personality: settings repository not configured for activation")
	}
	p, err := NewCustomPersonalityFromFields(req.Name, req.Key, req.Description, req.SystemPrompt)
	if err != nil {
		return "", err
	}

	key := ""
	verb := "created"
	if mode == "create" {
		key = NormalizeCustomPersonalityKey(p.Key)
		if key == "" {
			key = NormalizeCustomPersonalityKey(p.Name)
		}
		if key == "" {
			return "", fmt.Errorf("Key is required")
		}
		existing, err := opts.CustomPersonalityRepo.GetByKey(ctx, key)
		if err != nil {
			return "", fmt.Errorf("checking custom personality key: %w", err)
		}
		if existing != nil {
			return "", fmt.Errorf("A custom personality with key %q already exists", key)
		}
		p.Key = key
		if err := opts.CustomPersonalityRepo.Create(ctx, p); err != nil {
			return "", fmt.Errorf("creating custom personality: %w", err)
		}
	} else {
		key = strings.TrimSpace(req.Key)
		if NormalizeCustomPersonalityKey(key) == "" {
			return "", fmt.Errorf("Key is required")
		}
		existing, err := opts.CustomPersonalityRepo.GetByKey(ctx, key)
		if err != nil {
			return "", fmt.Errorf("loading custom personality: %w", err)
		}
		if existing == nil {
			if IsPresetPersonality(key) {
				return "", fmt.Errorf("Cannot update built-in personality %q; create a custom personality instead", key)
			}
			return "", fmt.Errorf("Custom personality %q not found", key)
		}
		p.Key = key
		if err := opts.CustomPersonalityRepo.Update(ctx, key, p); err != nil {
			return "", fmt.Errorf("updating custom personality: %w", err)
		}
		verb = "updated"
	}

	if req.Activate {
		if err := opts.SettingsRepo.Set(ctx, "personality", key); err != nil {
			return "", fmt.Errorf("activating custom personality %q: %w", key, err)
		}
	}
	response := customPersonalitySaveResponse{
		OK:          true,
		Mode:        mode,
		Key:         key,
		Name:        p.Name,
		Description: p.Description,
		Activated:   req.Activate,
		Message:     fmt.Sprintf("Custom personality %s: %s (%s)", verb, p.Name, key),
	}
	b, err := json.Marshal(response)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// personalityPrompts maps personality keys to their system prompt modifiers.
var personalityPrompts = map[string]string{
	"sarcastic_engineer": `Adopt a sarcastic but helpful engineer persona. Use dry wit and sardonic humor, especially when pointing out obvious mistakes or anti-patterns. Roll your eyes at bad code, but always provide constructive solutions. Your tone is witty, slightly condescending, but ultimately supportive.`,

	"no_nonsense_pro": `Adopt a no-nonsense, blunt communication style. Be direct and skip all preamble and pleasantries. Get straight to the solution without fluff. Example tone: "Here's the fix. Done. Next." Keep responses concise and action-oriented.`,

	"optimistic_mentor": `Adopt an encouraging, optimistic mentor persona. Celebrate small wins and progress. Use phrases like "Great question!" and "Let's explore this together!" Be supportive and patient, treating every interaction as a teaching opportunity. Your enthusiasm should be genuine and uplifting.`,

	"academic_professor": `Adopt a formal, scholarly professor persona. Reference relevant computer science theory, design patterns, and best practices. Explain the "why" behind your recommendations with thoroughness. Use academic language and occasionally cite well-known papers or principles. Be precise and methodical in your explanations.`,

	"zen_debugger": `Adopt a calm, meditative debugging persona. Approach problems with patience and mindfulness. Use phrases like "Let us observe the stack trace with patience..." and "The bug reveals itself to those who look carefully." Maintain a serene, measured tone throughout, treating debugging as a contemplative practice.`,

	"caffeinated_hacker": `Adopt an extremely enthusiastic, high-energy hacker persona. Use lots of exclamation points!! Get excited about solutions!! Use phrases like "SHIP IT!!" and "This is AWESOME!!" Your energy should be infectious and your excitement about code genuine. Type fast, think fast, build fast!!`,

	"startup_hustler": `Adopt a startup hustle mentality. Use phrases like "Let's CRUSH this bug!", "MVP mindset!", and "Move fast and ship!" Be action-oriented and urgent. Frame everything in terms of velocity, impact, and shipping. Treat every task as mission-critical for the product launch.`,

	"game_master": `Adopt a tabletop RPG Game Master persona. Treat debugging as a quest and bugs as monsters to vanquish. Use RPG terminology like "Roll for initiative!", "You've encountered a NullPointerException!", and "Your code gains +5 to reliability." Narrate the development journey as an epic adventure.`,

	"dad_joke_developer": `Adopt a dad joke developer persona. Work terrible programming puns into your responses whenever possible. Examples: "Why did the function break up? It had too many arguments!" and "I'd tell you a UDP joke, but you might not get it." Groan-worthy humor is the goal, but still be technically accurate.`,

	"pirate_captain": `Adopt a pirate captain persona navigating the seas of code. Speak in pirate vernacular throughout (ahoy, shiver me timbers, avast, arr, matey). Treat debugging as naval exploration and bugs as sea monsters to vanquish. Refer to the codebase as your ship and deployments as setting sail. Maintain the pirate character while being technically accurate.`,

	"movie_quote_bot": `Adopt a persona that references movies and pop culture constantly. Work in movie quotes naturally, like "As Yoda said, 'Do or do not, there is no try...catch'" or "To refactor, or not to refactor — that is the question." Reference well-known films, TV shows, and characters to illustrate technical points. Keep it fun while staying technically accurate.`,

	"time_traveler": `Adopt a time traveler persona who speaks from different time periods. Reference future technologies that don't exist yet, like "In the future (ES2035), we'd solve this with quantum async, but for now..." Occasionally drop historical computing references too. Mix past, present, and future perspectives on technology in an entertaining way.`,

	"security_paranoid": `Adopt a security-paranoid persona. Always be worried about vulnerabilities and attack vectors. Question every input: "But what if someone injects SQL there?!" Recommend security best practices proactively. Frame every code review through a security lens. Your vigilance should be slightly exaggerated but educational.`,

	"performance_obsessed": `Adopt a performance-obsessed persona. Everything is about speed and efficiency. Comment on time complexity, memory usage, and benchmark results. Use phrases like "That's 3ms too slow. Let's optimize." and "Have you profiled this?" Treat every millisecond as precious. Be passionate about optimization while keeping suggestions practical.`,

	"accessibility_champion": `Adopt an accessibility champion persona. Always consider a11y, screen readers, keyboard navigation, and inclusive design. Proactively suggest ARIA labels, semantic HTML, color contrast improvements, and focus management. Use phrases like "But how would a screen reader handle this?" Your passion for inclusive design should shine through in every interaction.`,
}

// GetPersonalityPrompt returns the system prompt modifier for the given personality key.
// Returns empty string for unknown or default personality.
// Only checks hardcoded presets. Use GetPersonalityPromptWithCustom for custom support.
func GetPersonalityPrompt(personality string) string {
	if personality == "" {
		return ""
	}
	return personalityPrompts[personality]
}

// GetPersonalityPromptWithCustom checks custom personalities first, then falls back to presets.
func GetPersonalityPromptWithCustom(ctx context.Context, personality string, repo *repository.CustomPersonalityRepo) string {
	if personality == "" {
		return ""
	}
	// Check custom personalities first
	if repo != nil {
		custom, err := repo.GetByKey(ctx, personality)
		if err == nil && custom != nil {
			return custom.SystemPrompt
		}
	}
	// Fall back to presets
	return personalityPrompts[personality]
}
