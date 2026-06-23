# Agent Guidelines

## CRITICAL: Do not commit or push until requested

Never run `git commit`, `git push`, `git reset`, `git rebase`, or any other git mutations unless the user explicitly requests it. The user wants to test changes before they are committed. Ask for confirmation each time before committing or pushing.

If the user says "commit and push", they are requesting it. Otherwise, stage changes but do not commit.

## RESPONSES 

- Keep responses concise and to the point - unless the user asks otherwise. 

## PLANING MODE

- Always ask clarifying questions
- Never assume design, tech stack or features
- Use deep-dive sub-agents to assist with research
- Use deep-dive sub-agents to review the different aspects of your plan before presenting to the user

## CHANGE / EDIT MODE

- Never implement features yourself when possible - use sub-agents!
- Identify changes from the plan that can be implemented in parallel, and user sub-agents to implement the features efficiently
- When using sub-agents to implement features, act as a coordinator only
- Use the best model for the task - premium models for complex tasks (like coding) and mid-tier models for simpler tasks, like documentation
- After completing features (largeor small), always run code quality checks

## TESTING 

- Use any testing tools, libraries available to the project for testing your changes
- Never assume your changes work, always test!
- If the project does not have any testing tools, scripts MCP tools, skills, etc. available for testing, ask the user whether testing should be skipped
