{
  allowedDomains = [
    "api.openai.com"
    "api.telegram.org"
    "wss-primary.slack.com"
    "wss-backup.slack.com"
    "slack.com"
  ];
  secrets1PPath = "vaults/Personal Agents/items/Agent Research";
  resources = {
    cpu = "500m";
    memory = "512Mi";
  };
}
