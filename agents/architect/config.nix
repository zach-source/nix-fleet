{
  allowedDomains = [
    "api.zai.com"
    "open.bigmodel.cn"
    "api.openai.com"
    "api.github.com"
    "wss-primary.slack.com"
    "wss-backup.slack.com"
    "slack.com"
  ];
  secrets1PPath = "vaults/Personal Agents/items/Agent Architect";
  resources = {
    cpu = "750m";
    memory = "768Mi";
  };
}
