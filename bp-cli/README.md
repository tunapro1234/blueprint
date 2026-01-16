# BLUEPRINT

> This section contains the philosophy and the rationale behind this project. Written by a human, translated by AI. For technical details, please scroll down.
## Note from the dev

There are 3 FUNDAMENTAL problems with current AI tools that blueprint solves. The first is the insufficiency of planning mode, the second is the small size of the context window, and the third is agent collaboration.

I have been using Claude Code since Opus 4.5 came out, and it is an incredible tool. However, when you want to build complex systems, the planning feature falls short. Blueprint makes the plan structure permanent and expands upon it; a new software paradigm is being born around this. I am also simultaneously developing the tools and environment that accompany this new paradigm.

Currently, we treat each folder as a scope and place a blueprint inside each one. The reason for this is that the context window isn't large enough; when you try to feed too much information to the model at once, it cannot act in a meaningful and coherent way. This will likely be solved in the future. I use the tool in all my own projects, and it will evolve alongside me and the developments in Claude Code.

Blueprint can also be thought of as a new kind of programming language. Just as C emerged when we were writing Assembly and took a lot of the workload off our hands, current AIs are taking the coding work from us. However, they are not yet as good as we are at imagining and designing systems (especially large and extensive systems, though this will likely change with the expansion of context windows or new transformer architectures).

At the same time, Blueprint provides a solution to the problem of agent collaboration and synchronization. Agents can no longer clash because what they need to do is clear, and everyone's domain is clearly defined.

Normally, I wouldn't expect such a major shift to gain wide adoption (and frankly, I still don't), but after seeing the speed at which AI writes code, it’s not hard to imagine all code being rewritten from scratch. Our speed of adopting new technologies has also changed significantly with tools like Claude Code.

### Visualization
Up until now, we have always treated code as text, but I see no reason for this to continue. In the past, when we were the ones writing the code, giving the computer a sequential list of tasks made perfect sense. However, the work we do now requires organizing agents, implying that many processes must be running simultaneously.

Historically, focusing on a single thing (the code you were writing) was paramount. Our IDEs reflect this perfectly; the code takes up almost the entire screen. But this is no longer what we need.

Since next-generation software development requires managing many contexts at once, Linux workspaces are incredibly useful (especially tiling window managers like dwm or hyprland), but even they are not enough. What we need is an interface like a Real-Time Strategy game. We need a livelier interface where we can see the status of every agent, engage with different tasks continuously, and "touch" the process. ((Because in the current system, the moment I switch to YouTube while waiting for an agent to finish a prompt, my focus is completely shattered. I hate this. I mean I REALLY hate this.))

### How I use this structure
Although Claude Code is incredibly good, heavy reasoning models like codex 5.2 max are generally better at detecting deep errors and edge cases, but they are so slow that they should ABSOLUTELY NOT be used in the planning phase.
 
The cleanest workflow seems to be: Use Claude Code as the planner to iterate quickly on new structures and plans, then use the reasoning model as the worker. Once the plan is complete, I ask the worker to validate it; letting the workers see the plan keeps the architecture grounded.

If this project gains traction, I plan to integrate this agent structure directly into the bp (blueprint-cli) tool and establish a multi-agent worker team. By developing a Worker + Tester + Judge (or similar) system, the implementation process could be streamlined significantly. Incredible agentic setups can be built around this. I’m also thinking of adding tunnels and different systems for inter-agent communication.

