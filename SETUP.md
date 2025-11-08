# Setup Guide

## Prerequisites

- Go 1.24 or later
- Discord account
- Anthropic or OpenAI API key

## Step 1: Create Discord Bot

1. Go to [Discord Developer Portal](https://discord.com/developers/applications)
2. Click "New Application"
3. Give it a name (e.g., "WingThing")
4. Go to "Bot" section
5. Click "Add Bot"
6. Copy the bot token (you'll need this for config.yaml)
7. Under "Privileged Gateway Intents", enable:
   - Message Content Intent
8. Go to "OAuth2" → "URL Generator"
9. Select scopes: `bot`
10. Select bot permissions: `Send Messages`, `Read Messages/View Channels`, `Read Message History`
11. Copy the generated URL and open it in browser to add bot to your server (or just DM it)

## Step 2: Get Your Discord User ID

1. Enable Developer Mode in Discord:
   - User Settings → App Settings → Advanced → Developer Mode
2. Right-click your username anywhere in Discord
3. Click "Copy User ID"
4. Save this ID for config.yaml

## Step 3: Configure WingThing

```bash
# Copy example config
cp config.example.yaml config.yaml

# Edit config.yaml with your values
# - discord.token: Your bot token from Step 1
# - discord.user_id: Your user ID from Step 2
# - llm.api_key: Your Anthropic or OpenAI API key
# - llm.provider: "anthropic" or "openai"
```

## Step 4: Build and Run

```bash
# Install dependencies
make deps

# Build
make build

# Run the bot
./wingthing bot
```

## Step 5: Test

1. Send a DM to your bot on Discord
2. Try commands:
   - `/start` - Introduction
   - `/help` - Show available commands
   - `/stats` - View your stats

## Step 6: Schedule Dreams (Optional)

To run Dreams synthesis automatically every night at 2am:

```bash
# Edit crontab
crontab -e

# Add this line (adjust path to your wingthing binary)
0 2 * * * /path/to/wingthing dream >> /path/to/wingthing.log 2>&1
```

Or create a systemd timer, or use whatever scheduling system you prefer.

## Troubleshooting

### Bot doesn't respond
- Check that the bot is online in Discord
- Verify `discord.user_id` matches your actual Discord user ID
- Make sure you're DMing the bot (not messaging in a server)
- Check logs for errors

### "Failed to load config"
- Ensure config.yaml exists and is valid YAML
- Check all required fields are filled in
- Try running with `--config` flag to specify path explicitly

### Database errors
- Make sure the directory for `database.path` exists
- Check file permissions

## Next Steps

Once the bot is running and responding:
1. Chat with it throughout the day
2. Manually run Dreams: `./wingthing dream`
3. Check that Morning Cards are generated
4. Iterate on prompts and functionality

## Development

```bash
# Run tests
make test

# Format code
make fmt

# Clean build artifacts
make clean

# Show all make targets
make help
```
