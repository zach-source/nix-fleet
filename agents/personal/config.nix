{
  allowedDomains = [
    "api.openai.com"
    "api.telegram.org"
    "gmail.googleapis.com"
    "www.googleapis.com"
    "oauth2.googleapis.com"
    "accounts.google.com"
  ];
  secrets1PPath = "vaults/Personal Agents/items/Agent Personal";
  resources = {
    cpu = "500m";
    memory = "512Mi";
  };
}
