{
  allowedDomains = [
    "api.openai.com"
    "api.github.com"
    "wss-primary.slack.com"
    "wss-backup.slack.com"
    "slack.com"
  ];
  secrets1PPath = "vaults/Personal Agents/items/Agent Sage";
  resources = {
    cpu = "1000m";
    memory = "1Gi";
  };
}
