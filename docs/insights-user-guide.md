# Insights User Guide

Use the Insights section in the sidebar to understand project activity after tasks, schedules, agents, and channels have started producing work.

## Sidebar Pages

| UI Label | What It Is For |
|---|---|
| Grades | Proactive insights, health checks, knowledge signals, and idea grading. |
| Pulse | Upcoming work and generated pulse summaries. |
| Reflection | Historical task activity and generated reflections. |
| Analytics | Outcomes, supporting task evidence, agent and model comparisons, learning signals, automations, and provider usage. |

## Analytics

Analytics is the quantitative view of whether project work is producing useful outcomes and where attention is needed. Open it from the Insights section of the sidebar, then select a time window and optional project filters. Each section uses explicit denominators so a completed run is not confused with an achieved goal or merged work.

### Overview And Outcomes

The Overview summarizes tasks worked on, goals achieved, work merged, skills used, and automation activity. Outcome trends and the funnel connect run success, goal achievement, first-run success, follow-up work, and merge completion. Improvement, attention, and actionable-exception cards surface slow, costly, repeatedly failing, or reworked tasks.

Open Outcomes for detailed success/failure, rework, follow-up, timing, and failure evidence. Use Supporting task evidence to drill into the tasks behind a metric instead of treating an aggregate chart as the final answer.

### Agents, Models, Automations, And Learning

Compare agents and model configurations by goal achievement, merge completion, first-run success, follow-up rate, runtime, reliability, and effort. The Automations view reports graph invocation and node behavior. Learning connects skill selection and usage to task outcomes so you can identify productive agent/skill pairings and enabled skills that may need clearer guidance or cleanup.

### Token Usage And Cost

| Chart / Table | What It Shows |
|---|---|
| Token Usage chart | Input, output, cached, and reasoning tokens over time, filterable by model. |
| Token Usage Breakdown table | Per-provider, per-model columns for input tokens, output tokens, cache tokens, reasoning tokens, total tokens, and estimated cost. |
| Model Breakdown by Tokens | Pie chart showing relative token share across configured models. |

### Provider Accounts

OAuth-connected provider accounts (Anthropic, OpenAI) show a usage snapshot card so you can see which account is consuming capacity.

Analytics charts render in the browser timezone so time-axis labels match local working hours.

Use a chart's expand control to inspect it in a larger modal preview. The preview retains chart tooltips; close it or press Escape to return to the dashboard.

## How Insights Fit The Workflow

Use the task board for live execution status. Use Insights when you want to step back and answer questions like:

- Are tasks reaching their goals, merging, or requiring rework?
- Which agents, models, skills, or automations produce the strongest outcomes?
- What work is coming up this week?
- What historical trends are emerging?
- Which model is consuming the most tokens or cost?

Use Analytics specifically to connect outcome and task evidence with provider spend, token consumption, execution performance, and learning across the project.

## Related Pages

For the full reference see <a href="https://docs.openvibely.ai/insights" target="_blank" rel="noopener noreferrer">Insights</a> on the docs site.

| Area | See Also |
|---|---|
| Tasks | Task activity drives most insight data. |
| Alerts | Failures and follow-up events may also appear in trends. |
| Models | Model choice directly affects token usage and cost figures in Analytics. |
| Workers | Execution rate and duration trends reflect worker capacity and queue pressure. |
