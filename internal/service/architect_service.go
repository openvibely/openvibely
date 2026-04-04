package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/util"
)

type ArchitectService struct {
	architectRepo *repository.ArchitectRepo
	taskRepo      *repository.TaskRepo
	projectRepo   *repository.ProjectRepo
	llmConfigRepo *repository.LLMConfigRepo
	llmSvc        *LLMService
}

func NewArchitectService(
	architectRepo *repository.ArchitectRepo,
	taskRepo *repository.TaskRepo,
	projectRepo *repository.ProjectRepo,
	llmConfigRepo *repository.LLMConfigRepo,
) *ArchitectService {
	return &ArchitectService{
		architectRepo: architectRepo,
		taskRepo:      taskRepo,
		projectRepo:   projectRepo,
		llmConfigRepo: llmConfigRepo,
	}
}

// SetLLMService breaks circular dependency (same pattern as CollisionService)
func (s *ArchitectService) SetLLMService(llmSvc *LLMService) {
	s.llmSvc = llmSvc
}

// --- Dashboard ---

func (s *ArchitectService) GetDashboard(ctx context.Context, projectID string) (*models.ArchitectDashboardData, error) {
	data := &models.ArchitectDashboardData{}

	active, err := s.architectRepo.ListSessionsByProject(ctx, projectID, models.ArchitectStatusActive)
	if err != nil {
		log.Printf("[architect-svc] error listing active sessions: %v", err)
	}
	if active == nil {
		active = []models.ArchitectSession{}
	}
	data.ActiveSessions = active

	completed, err := s.architectRepo.ListSessionsByProject(ctx, projectID, models.ArchitectStatusCompleted)
	if err != nil {
		log.Printf("[architect-svc] error listing completed sessions: %v", err)
	}
	if completed == nil {
		completed = []models.ArchitectSession{}
	}
	data.CompletedSessions = completed

	templates, err := s.architectRepo.ListTemplates(ctx)
	if err != nil {
		log.Printf("[architect-svc] error listing templates: %v", err)
	}
	if templates == nil {
		templates = []models.ArchitectTemplate{}
	}
	data.Templates = templates

	return data, nil
}

// --- Session Management ---

func (s *ArchitectService) CreateSession(ctx context.Context, projectID, title, description string, templateID *string) (*models.ArchitectSession, error) {
	session := &models.ArchitectSession{
		ProjectID:   projectID,
		Title:       title,
		Description: description,
		Status:      models.ArchitectStatusActive,
		Phase:       models.PhaseVisionRefinement,
		VisionData:  "{}",
		ArchData:    "{}",
		RiskData:    "{}",
		PhaseData:   "{}",
		DepData:     "{}",
		EstData:     "{}",
		TemplateID:  templateID,
	}

	// If starting from a template, pre-populate data
	if templateID != nil {
		tmpl, err := s.architectRepo.GetTemplate(ctx, *templateID)
		if err != nil {
			log.Printf("[architect-svc] error getting template: %v", err)
		} else if tmpl != nil {
			session.VisionData = tmpl.VisionData
			session.ArchData = tmpl.ArchData
			if err := s.architectRepo.IncrementTemplateUsage(ctx, tmpl.ID); err != nil {
				log.Printf("[architect-svc] error incrementing template usage: %v", err)
			}
		}
	}

	if err := s.architectRepo.CreateSession(ctx, session); err != nil {
		return nil, fmt.Errorf("creating session: %w", err)
	}

	// Add initial assistant message
	msg := &models.ArchitectMessage{
		SessionID: session.ID,
		Role:      "assistant",
		Content:   buildInitialArchitectQuestion(title, description),
		Phase:     models.PhaseVisionRefinement,
	}
	if err := s.architectRepo.CreateMessage(ctx, msg); err != nil {
		log.Printf("[architect-svc] error creating initial message: %v", err)
	}

	return session, nil
}

func (s *ArchitectService) GetSessionDetail(ctx context.Context, sessionID string) (*models.ArchitectSessionDetail, error) {
	session, err := s.architectRepo.GetSession(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("getting session: %w", err)
	}
	if session == nil {
		return nil, fmt.Errorf("session not found")
	}

	messages, err := s.architectRepo.ListMessages(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("listing messages: %w", err)
	}

	tasks, err := s.architectRepo.ListTasksBySession(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("listing tasks: %w", err)
	}

	return &models.ArchitectSessionDetail{
		Session:  *session,
		Messages: messages,
		Tasks:    tasks,
	}, nil
}

func (s *ArchitectService) DeleteSession(ctx context.Context, sessionID string) error {
	return s.architectRepo.DeleteSession(ctx, sessionID)
}

func (s *ArchitectService) AbandonSession(ctx context.Context, sessionID string) error {
	session, err := s.architectRepo.GetSession(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("getting session: %w", err)
	}
	if session == nil {
		return fmt.Errorf("session not found")
	}
	session.Status = models.ArchitectStatusAbandoned
	return s.architectRepo.UpdateSession(ctx, session)
}

// --- Multi-turn Conversation ---

func (s *ArchitectService) SendMessage(ctx context.Context, sessionID, userMessage string) (*models.ArchitectMessage, error) {
	session, err := s.architectRepo.GetSession(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("getting session: %w", err)
	}
	if session == nil {
		return nil, fmt.Errorf("session not found")
	}

	// Save user message
	userMsg := &models.ArchitectMessage{
		SessionID: session.ID,
		Role:      "user",
		Content:   userMessage,
		Phase:     session.Phase,
	}
	if err := s.architectRepo.CreateMessage(ctx, userMsg); err != nil {
		return nil, fmt.Errorf("saving user message: %w", err)
	}

	// Get conversation history
	messages, err := s.architectRepo.ListMessages(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("listing messages: %w", err)
	}

	// Generate AI response based on current phase
	aiResponse, err := s.generateAIResponse(ctx, session, messages, userMessage)
	if err != nil {
		return nil, fmt.Errorf("generating AI response: %w", err)
	}

	// Save assistant message
	assistantMsg := &models.ArchitectMessage{
		SessionID: session.ID,
		Role:      "assistant",
		Content:   aiResponse,
		Phase:     session.Phase,
	}
	if err := s.architectRepo.CreateMessage(ctx, assistantMsg); err != nil {
		return nil, fmt.Errorf("saving assistant message: %w", err)
	}

	return assistantMsg, nil
}

// --- Phase Advancement ---

func (s *ArchitectService) AdvancePhase(ctx context.Context, sessionID string) (*models.ArchitectSession, error) {
	session, err := s.architectRepo.GetSession(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("getting session: %w", err)
	}
	if session == nil {
		return nil, fmt.Errorf("session not found")
	}

	nextPhase := getNextArchitectPhase(session.Phase)
	if nextPhase == session.Phase {
		return session, nil // already at final phase
	}

	// Process current phase data before advancing
	if err := s.processPhaseCompletion(ctx, session); err != nil {
		log.Printf("[architect-svc] error processing phase completion: %v", err)
	}

	session.Phase = nextPhase
	if nextPhase == models.PhaseComplete {
		session.Status = models.ArchitectStatusCompleted
	}
	if err := s.architectRepo.UpdateSession(ctx, session); err != nil {
		return nil, fmt.Errorf("updating session phase: %w", err)
	}

	// Add phase transition message
	msg := &models.ArchitectMessage{
		SessionID: session.ID,
		Role:      "assistant",
		Content:   getArchitectPhaseIntroMessage(nextPhase),
		Phase:     nextPhase,
	}
	if err := s.architectRepo.CreateMessage(ctx, msg); err != nil {
		log.Printf("[architect-svc] error creating phase intro message: %v", err)
	}

	return session, nil
}

// --- AI-Powered Analysis ---

func (s *ArchitectService) GenerateArchitecture(ctx context.Context, sessionID string) (*models.ArchRecommendation, error) {
	session, err := s.architectRepo.GetSession(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("getting session: %w", err)
	}
	if session == nil {
		return nil, fmt.Errorf("session not found")
	}

	project, err := s.projectRepo.GetByID(ctx, session.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("getting project: %w", err)
	}

	agent, err := s.llmConfigRepo.GetDefault(ctx)
	if err != nil || agent == nil {
		return nil, fmt.Errorf("no default AI agent configured")
	}

	prompt := buildArchitecturePrompt(session.VisionData, session.Title, session.Description)
	output, _, err := s.llmSvc.CallAgentDirect(ctx, prompt, nil, *agent, project.RepoPath)
	if err != nil {
		return nil, fmt.Errorf("AI architecture analysis failed: %w", err)
	}

	var rec models.ArchRecommendation
	if err := parseArchitectJSONFromAI(output, &rec); err != nil {
		rec = models.ArchRecommendation{Summary: output}
	}

	// Save to session
	archJSON, _ := json.Marshal(rec)
	session.ArchData = string(archJSON)
	if err := s.architectRepo.UpdateSession(ctx, session); err != nil {
		log.Printf("[architect-svc] error saving arch data: %v", err)
	}

	return &rec, nil
}

func (s *ArchitectService) GenerateRiskAnalysis(ctx context.Context, sessionID string) (*models.RiskAnalysis, error) {
	session, err := s.architectRepo.GetSession(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("getting session: %w", err)
	}
	if session == nil {
		return nil, fmt.Errorf("session not found")
	}

	project, err := s.projectRepo.GetByID(ctx, session.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("getting project: %w", err)
	}

	agent, err := s.llmConfigRepo.GetDefault(ctx)
	if err != nil || agent == nil {
		return nil, fmt.Errorf("no default AI agent configured")
	}

	prompt := buildRiskAnalysisPrompt(session.VisionData, session.ArchData, session.Title)
	output, _, err := s.llmSvc.CallAgentDirect(ctx, prompt, nil, *agent, project.RepoPath)
	if err != nil {
		return nil, fmt.Errorf("AI risk analysis failed: %w", err)
	}

	var analysis models.RiskAnalysis
	if err := parseArchitectJSONFromAI(output, &analysis); err != nil {
		analysis = models.RiskAnalysis{Summary: output}
	}

	riskJSON, _ := json.Marshal(analysis)
	session.RiskData = string(riskJSON)
	if err := s.architectRepo.UpdateSession(ctx, session); err != nil {
		log.Printf("[architect-svc] error saving risk data: %v", err)
	}

	return &analysis, nil
}

func (s *ArchitectService) GenerateTaskPlan(ctx context.Context, sessionID string) ([]models.ArchitectTask, error) {
	session, err := s.architectRepo.GetSession(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("getting session: %w", err)
	}
	if session == nil {
		return nil, fmt.Errorf("session not found")
	}

	project, err := s.projectRepo.GetByID(ctx, session.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("getting project: %w", err)
	}

	agent, err := s.llmConfigRepo.GetDefault(ctx)
	if err != nil || agent == nil {
		return nil, fmt.Errorf("no default AI agent configured")
	}

	prompt := buildTaskPlanPrompt(session.VisionData, session.ArchData, session.RiskData, session.Title)
	output, _, err := s.llmSvc.CallAgentDirect(ctx, prompt, nil, *agent, project.RepoPath)
	if err != nil {
		return nil, fmt.Errorf("AI task planning failed: %w", err)
	}

	var taskDefs []struct {
		Title      string   `json:"title"`
		Prompt     string   `json:"prompt"`
		Phase      string   `json:"phase"`
		Priority   int      `json:"priority"`
		DependsOn  []string `json:"depends_on"`
		IsBlocking bool     `json:"is_blocking"`
		Complexity string   `json:"complexity"`
		EstHours   float64  `json:"est_hours"`
	}
	if err := parseArchitectJSONFromAI(output, &taskDefs); err != nil {
		return nil, fmt.Errorf("failed to parse task plan: %w", err)
	}

	// Clear existing tasks for this session
	if err := s.architectRepo.DeleteTasksBySession(ctx, sessionID); err != nil {
		log.Printf("[architect-svc] error clearing old tasks: %v", err)
	}

	var tasks []models.ArchitectTask
	for _, td := range taskDefs {
		depsJSON, _ := json.Marshal(td.DependsOn)
		phase := models.ArchitectTaskPhase(td.Phase)
		if phase != models.TaskPhaseMVP && phase != models.TaskPhaseTwo && phase != models.TaskPhaseThree && phase != models.TaskPhaseMitigation {
			phase = models.TaskPhaseMVP
		}
		complexity := td.Complexity
		if complexity != "low" && complexity != "medium" && complexity != "high" {
			complexity = "medium"
		}
		priority := td.Priority
		if priority < 1 || priority > 4 {
			priority = 2
		}

		task := &models.ArchitectTask{
			SessionID:  sessionID,
			Title:      td.Title,
			Prompt:     td.Prompt,
			Phase:      phase,
			Priority:   priority,
			DependsOn:  string(depsJSON),
			IsBlocking: td.IsBlocking,
			Complexity: complexity,
			EstHours:   td.EstHours,
		}
		if err := s.architectRepo.CreateTask(ctx, task); err != nil {
			log.Printf("[architect-svc] error creating task %q: %v", td.Title, err)
			continue
		}
		tasks = append(tasks, *task)
	}

	// Save phase data
	breakdown := buildArchitectPhaseBreakdown(tasks)
	phaseJSON, _ := json.Marshal(breakdown)
	session.PhaseData = string(phaseJSON)
	if err := s.architectRepo.UpdateSession(ctx, session); err != nil {
		log.Printf("[architect-svc] error saving phase data: %v", err)
	}

	// Save estimate data
	estimate := computeArchitectResourceEstimate(tasks)
	estJSON, _ := json.Marshal(estimate)
	session.EstData = string(estJSON)
	if err := s.architectRepo.UpdateSession(ctx, session); err != nil {
		log.Printf("[architect-svc] error saving estimate data: %v", err)
	}

	return tasks, nil
}

// --- Task Activation ---

func (s *ArchitectService) ActivatePhase(ctx context.Context, sessionID string, phase models.ArchitectTaskPhase, projectID string) (int, error) {
	tasks, err := s.architectRepo.ListTasksByPhase(ctx, sessionID, phase)
	if err != nil {
		return 0, fmt.Errorf("listing tasks: %w", err)
	}

	activated := 0
	for _, at := range tasks {
		if at.IsActivated {
			continue
		}
		realTask := &models.Task{
			ProjectID: projectID,
			Title:     at.Title,
			Prompt:    at.Prompt,
			Category:  models.CategoryBacklog,
			Priority:  at.Priority,
			Status:    models.StatusPending,
		}
		if err := s.taskRepo.Create(ctx, realTask); err != nil {
			log.Printf("[architect-svc] error creating real task for %q: %v", at.Title, err)
			continue
		}
		if err := s.architectRepo.ActivateTask(ctx, at.ID, realTask.ID); err != nil {
			log.Printf("[architect-svc] error activating task %q: %v", at.Title, err)
			continue
		}
		activated++
	}

	return activated, nil
}

// --- Templates ---

func (s *ArchitectService) SaveAsTemplate(ctx context.Context, sessionID string, name, description, category string) (*models.ArchitectTemplate, error) {
	session, err := s.architectRepo.GetSession(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("getting session: %w", err)
	}
	if session == nil {
		return nil, fmt.Errorf("session not found")
	}

	tasks, err := s.architectRepo.ListTasksBySession(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("listing tasks: %w", err)
	}
	tasksJSON, _ := json.Marshal(tasks)

	tmpl := &models.ArchitectTemplate{
		Name:        name,
		Description: description,
		Category:    category,
		VisionData:  session.VisionData,
		ArchData:    session.ArchData,
		TasksData:   string(tasksJSON),
	}
	if err := s.architectRepo.CreateTemplate(ctx, tmpl); err != nil {
		return nil, fmt.Errorf("creating template: %w", err)
	}
	return tmpl, nil
}

func (s *ArchitectService) DeleteTemplate(ctx context.Context, templateID string) error {
	return s.architectRepo.DeleteTemplate(ctx, templateID)
}

func (s *ArchitectService) ListTemplates(ctx context.Context) ([]models.ArchitectTemplate, error) {
	return s.architectRepo.ListTemplates(ctx)
}

// --- Internal helpers ---

func (s *ArchitectService) generateAIResponse(ctx context.Context, session *models.ArchitectSession, messages []models.ArchitectMessage, userMessage string) (string, error) {
	if s.llmSvc == nil {
		return generateArchitectFallbackResponse(session.Phase, userMessage), nil
	}

	project, err := s.projectRepo.GetByID(ctx, session.ProjectID)
	if err != nil {
		return "", fmt.Errorf("getting project: %w", err)
	}

	agent, err := s.llmConfigRepo.GetDefault(ctx)
	if err != nil || agent == nil {
		return generateArchitectFallbackResponse(session.Phase, userMessage), nil
	}

	prompt := buildArchitectConversationPrompt(session, messages, userMessage)
	output, _, err := s.llmSvc.CallAgentDirect(ctx, prompt, nil, *agent, project.RepoPath)
	if err != nil {
		log.Printf("[architect-svc] AI response failed, using fallback: %v", err)
		return generateArchitectFallbackResponse(session.Phase, userMessage), nil
	}
	return output, nil
}

func (s *ArchitectService) processPhaseCompletion(ctx context.Context, session *models.ArchitectSession) error {
	switch session.Phase {
	case models.PhaseVisionRefinement:
		// Extract vision summary from conversation
		messages, err := s.architectRepo.ListMessages(ctx, session.ID)
		if err != nil {
			return err
		}
		summary := extractArchitectVisionSummary(messages)
		session.VisionData = summary
		return s.architectRepo.UpdateSession(ctx, session)
	}
	return nil
}

func getNextArchitectPhase(current models.ArchitectPhase) models.ArchitectPhase {
	phases := []models.ArchitectPhase{
		models.PhaseVisionRefinement,
		models.PhaseArchitecture,
		models.PhaseRiskAnalysis,
		models.PhasePhasing,
		models.PhaseDependencies,
		models.PhaseEstimation,
		models.PhaseReview,
		models.PhaseComplete,
	}
	for i, p := range phases {
		if p == current && i < len(phases)-1 {
			return phases[i+1]
		}
	}
	return current
}

func buildInitialArchitectQuestion(title, description string) string {
	var sb strings.Builder
	sb.WriteString("Welcome to **Architect**! I'll help you plan your project from the ground up.\n\n")
	if title != "" {
		sb.WriteString(fmt.Sprintf("I see you're working on **%s**", title))
		if description != "" {
			sb.WriteString(fmt.Sprintf(": %s", description))
		}
		sb.WriteString(".\n\n")
	}
	sb.WriteString("Let's start by understanding your vision. Please tell me:\n\n")
	sb.WriteString("1. **What problem does this project solve?** Who are the target users?\n")
	sb.WriteString("2. **What are your key success metrics?** How will you know the project is successful?\n")
	sb.WriteString("3. **What's your timeline?** Any hard deadlines?\n")
	sb.WriteString("4. **What technical preferences do you have?** (languages, frameworks, cloud providers)\n")
	sb.WriteString("5. **What constraints should I know about?** (budget, team size, existing systems)\n\n")
	sb.WriteString("Feel free to answer all at once or we can discuss each one step by step.")
	return sb.String()
}

func buildArchitectConversationPrompt(session *models.ArchitectSession, messages []models.ArchitectMessage, userMessage string) string {
	var sb strings.Builder
	sb.WriteString("You are a project planning assistant in Architect mode. You are helping the user plan a software project.\n\n")
	sb.WriteString(fmt.Sprintf("Project: %s\n", session.Title))
	sb.WriteString(fmt.Sprintf("Description: %s\n", session.Description))
	sb.WriteString(fmt.Sprintf("Current Phase: %s\n\n", session.Phase))

	sb.WriteString("Previous conversation:\n")
	for _, msg := range messages {
		sb.WriteString(fmt.Sprintf("[%s]: %s\n\n", msg.Role, util.Truncate(msg.Content, 500)))
	}
	sb.WriteString(fmt.Sprintf("\nUser's latest message: %s\n\n", userMessage))

	switch session.Phase {
	case models.PhaseVisionRefinement:
		sb.WriteString("Continue asking clarifying questions about the project vision. Focus on goals, target users, constraints, success metrics, timeline, and technical preferences. When you feel you have enough information, say 'I have a good understanding of your vision. Ready to move to the Architecture phase when you are.'\n")
	case models.PhaseArchitecture:
		sb.WriteString("Discuss architecture recommendations based on the vision data. Help the user understand trade-offs between different approaches.\n")
	case models.PhaseRiskAnalysis:
		sb.WriteString("Discuss potential risks and mitigation strategies for the project.\n")
	case models.PhasePhasing:
		sb.WriteString("Discuss how to break the project into MVP, Phase 2, and Phase 3. Help prioritize features.\n")
	default:
		sb.WriteString("Continue the project planning conversation. Be helpful and specific.\n")
	}
	sb.WriteString("Respond naturally in markdown format. Be concise but thorough.")
	return sb.String()
}

func buildArchitecturePrompt(visionData, title, description string) string {
	return fmt.Sprintf(`You are a software architect. Analyze the following project and provide architecture recommendations.

Project: %s
Description: %s
Vision Data: %s

Respond with a JSON object in this exact format:
{
  "tech_stack": [
    {"name": "Technology", "description": "Why", "pros": ["pro1"], "cons": ["con1"], "recommended": true}
  ],
  "architecture": [
    {"name": "Pattern", "description": "Description", "pros": ["pro1"], "cons": ["con1"], "recommended": true}
  ],
  "patterns": [
    {"name": "Design Pattern", "description": "Why", "pros": ["pro1"], "cons": ["con1"], "recommended": true}
  ],
  "infrastructure": [
    {"name": "Service", "description": "Purpose", "pros": ["pro1"], "cons": ["con1"], "recommended": true}
  ],
  "summary": "Overall architecture recommendation summary"
}

Provide 2-3 options per category with clear pros/cons. Mark one as recommended.
IMPORTANT: Return ONLY the JSON object, no markdown fences or extra text.`, title, description, visionData)
}

func buildRiskAnalysisPrompt(visionData, archData, title string) string {
	return fmt.Sprintf(`You are a project risk analyst. Analyze the following project for potential risks.

Project: %s
Vision: %s
Architecture: %s

Respond with a JSON object:
{
  "risks": [
    {
      "category": "technical|scaling|security|timeline|resource",
      "title": "Risk Title",
      "description": "Detailed description",
      "severity": "low|medium|high|critical",
      "likelihood": "low|medium|high",
      "mitigation": "How to address this risk"
    }
  ],
  "summary": "Overall risk assessment summary"
}

Identify 5-8 risks across different categories. Be specific and actionable.
IMPORTANT: Return ONLY the JSON object, no markdown fences or extra text.`, title, visionData, archData)
}

func buildTaskPlanPrompt(visionData, archData, riskData, title string) string {
	return fmt.Sprintf(`You are a project planning expert. Generate a comprehensive task plan.

Project: %s
Vision: %s
Architecture: %s
Risks: %s

Generate tasks organized into phases. Respond with a JSON array:
[
  {
    "title": "Short task title",
    "prompt": "Detailed instructions for an AI agent to execute this task",
    "phase": "mvp|phase_2|phase_3|mitigation",
    "priority": 1-4,
    "depends_on": [],
    "is_blocking": false,
    "complexity": "low|medium|high",
    "est_hours": 4.0
  }
]

Guidelines:
- MVP: 8-12 core tasks for minimum viable product
- Phase 2: 5-8 enhancement tasks
- Phase 3: 3-5 polish/optimization tasks
- Mitigation: 2-4 tasks addressing identified risks
- Priority: 4=urgent, 3=high, 2=normal, 1=low
- Include clear, actionable prompts suitable for an AI coding agent
- Mark tasks that block others as is_blocking: true

IMPORTANT: Return ONLY the JSON array, no markdown fences or extra text.`, title, visionData, archData, riskData)
}

func generateArchitectFallbackResponse(phase models.ArchitectPhase, userMessage string) string {
	switch phase {
	case models.PhaseVisionRefinement:
		return "Thank you for sharing that. Could you tell me more about:\n- Who are your target users?\n- What's your timeline?\n- Any technical preferences?\n\nWhen you're ready, click **Next Phase** to move to Architecture recommendations."
	case models.PhaseArchitecture:
		return "Based on what you've shared, I'd recommend exploring the architecture options. Click **Generate Architecture** to get AI-powered recommendations with pros and cons for each option."
	case models.PhaseRiskAnalysis:
		return "Let's identify potential risks. Click **Analyze Risks** to get a comprehensive risk assessment with mitigation strategies."
	default:
		return "Thank you for the input. Let's continue planning your project. You can advance to the next phase when ready."
	}
}

func extractArchitectVisionSummary(messages []models.ArchitectMessage) string {
	var userInputs []string
	for _, msg := range messages {
		if msg.Role == "user" && msg.Phase == models.PhaseVisionRefinement {
			userInputs = append(userInputs, msg.Content)
		}
	}
	if len(userInputs) == 0 {
		return "{}"
	}
	data := models.VisionRefinementData{
		Summary: strings.Join(userInputs, "\n\n"),
	}
	jsonData, _ := json.Marshal(data)
	return string(jsonData)
}

func getArchitectPhaseIntroMessage(phase models.ArchitectPhase) string {
	switch phase {
	case models.PhaseArchitecture:
		return "**Architecture Phase**\n\nNow let's design the architecture for your project. Click **Generate Architecture** to get AI-powered technology stack and architecture recommendations with pros and cons for each option. You can also ask me questions about architectural decisions."
	case models.PhaseRiskAnalysis:
		return "**Risk Analysis Phase**\n\nLet's identify potential risks in your project. Click **Analyze Risks** to get a comprehensive assessment of technical, scaling, security, and timeline risks with mitigation strategies."
	case models.PhasePhasing:
		return "**Feature Phasing**\n\nTime to break your project into phases. Click **Generate Task Plan** to create an MVP, Phase 2, and Phase 3 breakdown with concrete tasks for each phase."
	case models.PhaseDependencies:
		return "**Dependencies & Critical Path**\n\nLet's review the task dependencies and identify the critical path. The dependency graph shows which tasks block others and where parallel work is possible."
	case models.PhaseEstimation:
		return "**Resource Estimation**\n\nHere are the estimated resources needed for your project, broken down by phase. Review the estimates and adjust as needed."
	case models.PhaseReview:
		return "**Final Review**\n\nYour project plan is ready! Review the complete plan below. You can activate task phases to add them to your task board, or save this plan as a template for future use."
	case models.PhaseComplete:
		return "**Planning Complete!**\n\nYour project has been planned. Go to your task board to see the activated tasks."
	default:
		return "Moving to the next phase..."
	}
}

func parseArchitectJSONFromAI(output string, target interface{}) error {
	// Try direct parse
	if err := json.Unmarshal([]byte(output), target); err == nil {
		return nil
	}

	// Try extracting as object first, then as array
	if cleaned := util.ExtractJSONObject(output); cleaned != "" {
		return json.Unmarshal([]byte(cleaned), target)
	}
	if cleaned := util.ExtractJSONArray(output); cleaned != "" {
		return json.Unmarshal([]byte(cleaned), target)
	}

	return fmt.Errorf("no JSON found in response")
}

func buildArchitectPhaseBreakdown(tasks []models.ArchitectTask) models.PhaseBreakdown {
	breakdown := models.PhaseBreakdown{
		MVP:    models.PhaseDetail{Name: "MVP", Description: "Minimum Viable Product"},
		Phase2: models.PhaseDetail{Name: "Phase 2", Description: "Enhanced Features"},
		Phase3: models.PhaseDetail{Name: "Phase 3", Description: "Polish & Optimization"},
	}
	for _, t := range tasks {
		switch t.Phase {
		case models.TaskPhaseMVP:
			breakdown.MVP.Features = append(breakdown.MVP.Features, t.Title)
			breakdown.MVP.EstWeeks += int(t.EstHours / 40)
		case models.TaskPhaseTwo:
			breakdown.Phase2.Features = append(breakdown.Phase2.Features, t.Title)
			breakdown.Phase2.EstWeeks += int(t.EstHours / 40)
		case models.TaskPhaseThree:
			breakdown.Phase3.Features = append(breakdown.Phase3.Features, t.Title)
			breakdown.Phase3.EstWeeks += int(t.EstHours / 40)
		}
	}
	// Ensure minimum 1 week per non-empty phase
	if len(breakdown.MVP.Features) > 0 && breakdown.MVP.EstWeeks == 0 {
		breakdown.MVP.EstWeeks = 1
	}
	if len(breakdown.Phase2.Features) > 0 && breakdown.Phase2.EstWeeks == 0 {
		breakdown.Phase2.EstWeeks = 1
	}
	if len(breakdown.Phase3.Features) > 0 && breakdown.Phase3.EstWeeks == 0 {
		breakdown.Phase3.EstWeeks = 1
	}
	return breakdown
}

func computeArchitectResourceEstimate(tasks []models.ArchitectTask) models.ResourceEstimate {
	est := models.ResourceEstimate{
		ByPhase: make(map[string]models.PhaseEstimate),
	}
	for _, t := range tasks {
		est.TotalHours += t.EstHours
		est.TotalTasks++

		phase := string(t.Phase)
		pe := est.ByPhase[phase]
		pe.Tasks++
		pe.Hours += t.EstHours
		pe.Weeks = int(pe.Hours/40) + 1
		est.ByPhase[phase] = pe
	}

	switch {
	case est.TotalTasks <= 10:
		est.Complexity = "simple"
	case est.TotalTasks <= 25:
		est.Complexity = "moderate"
	default:
		est.Complexity = "complex"
	}

	est.AIAPICost = fmt.Sprintf("~$%.0f-$%.0f (estimated based on %d tasks)", float64(est.TotalTasks)*0.05, float64(est.TotalTasks)*0.20, est.TotalTasks)
	est.Summary = fmt.Sprintf("%d tasks, %.0f estimated hours, %s complexity", est.TotalTasks, est.TotalHours, est.Complexity)

	return est
}
