import { z } from 'zod';

const ProbeUrlSchema = z
  .string()
  .trim()
  .max(512, 'pages.probes.errors.url')
  .refine((v) => v === '' || /^https?:\/\/[^\s/]+/.test(v), 'pages.probes.errors.url');

export const ReachProbeFormSchema = z.object({
  name: z
    .string()
    .trim()
    .min(1, 'pages.probes.errors.name')
    .max(64, 'pages.probes.errors.name')
    .refine((v) => !/[<>&]/.test(v), 'pages.probes.errors.name'),
  enabled: z.boolean(),
  inboundIds: z.array(z.number().int()).min(1, 'pages.probes.errors.inbounds').max(32),
  testUrl: ProbeUrlSchema,
  directUrl: ProbeUrlSchema,
  intervalMin: z.number().int().min(1, 'pages.probes.errors.interval').max(1440),
});

export type ReachProbeFormValues = z.infer<typeof ReachProbeFormSchema>;
