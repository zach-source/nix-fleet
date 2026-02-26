{
  allowedDomains = [
    "api.anthropic.com"
    "api.openai.com"
    "api.github.com"
    "wss-primary.slack.com"
    "wss-backup.slack.com"
    "slack.com"
  ];
  secrets1PPath = "vaults/Personal Agents/items/Agent Orchestrator";
  resources = {
    cpu = "500m";
    memory = "512Mi";
  };
}
